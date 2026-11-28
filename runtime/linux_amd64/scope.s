# runtime/linux_amd64/scope.s — zero-libc, zero-GC structured multicore
# concurrency over raw Linux clone(2) + futex(2) + bounded mmap(2)/munmap(2).
#
# fc_scope header layout (at base of mmap region):
#   +0:   max_tasks       (u64)
#   +8:   total_mmap_size (u64)
#   +16:  lock            (u32 spinlock: 0 = unlocked, 1 = locked)
#   +20:  active          (u32 count of running kernel threads; futex word)
#   +24:  free_count      (u32 count of available slots; futex word)
#   +28:  pad             (u32)
#   +32:  stacks_base     (u64 pointer to slot 0 stack bottom)
#   +40:  free_slots[]    (u32[max_tasks] stack of free slot indices)

.text

.equ SYS_mmap,    9
.equ SYS_munmap,  11
.equ SYS_clone,   56
.equ SYS_exit,    60
.equ SYS_futex,   202

.equ PROT_RW,     3
.equ MAP_ANON,    0x22
.equ CLONE_THREAD_FLAGS, 0x00010f00 # CLONE_VM|CLONE_FS|CLONE_FILES|CLONE_SIGHAND|CLONE_THREAD
.equ FUTEX_WAIT_PRIVATE, 128
.equ FUTEX_WAKE_PRIVATE, 129
.equ SLOT_STACK_SIZE,    (256 * 1024)

.equ ERR_NIL,       0
.equ ERR_ARENAFULL, 9

# internal spinlock acquire/release on 16(%rdi)
.L_scope_lock:
    movl    $1, %eax
    xchgl   %eax, 16(%rdi)
    testl   %eax, %eax
    jz      .L_sl_got
    pause
    jmp     .L_scope_lock
.L_sl_got:
    ret

.L_scope_unlock:
    movl    $0, 16(%rdi)
    ret

# _fc_rt_scope_new(%rdi = max_tasks) -> %rax = scope_ptr
.global _fc_rt_scope_new
_fc_rt_scope_new:
    pushq   %rbx
    pushq   %r12
    movq    %rdi, %rbx        # max_tasks
    testq   %rbx, %rbx
    jnz     .L_snew_nz
    movq    $1, %rbx
.L_snew_nz:
    # header_size = alignUp(40 + max_tasks * 4, 4096)
    leaq    40(,%rbx,4), %r12
    addq    $4095, %r12
    andq    $-4096, %r12
    # total_size = header_size + max_tasks * SLOT_STACK_SIZE
    movq    %rbx, %rsi
    imulq   $SLOT_STACK_SIZE, %rsi, %rsi
    addq    %r12, %rsi        # rsi = total_size
    pushq   %rsi
    movq    $SYS_mmap, %rax
    xorq    %rdi, %rdi
    movq    $PROT_RW, %rdx
    movq    $MAP_ANON, %r10
    movq    $-1, %r8
    xorq    %r9, %r9
    syscall
    popq    %rsi
    cmpq    $-4095, %rax
    jae     .L_snew_fail
    # Initialize header
    movq    %rbx, 0(%rax)     # max_tasks
    movq    %rsi, 8(%rax)     # total_mmap_size
    movl    $0, 16(%rax)      # lock
    movl    $0, 20(%rax)      # active
    movl    %ebx, 24(%rax)    # free_count = max_tasks
    leaq    (%rax, %r12), %rdx
    movq    %rdx, 32(%rax)    # stacks_base
    # Fill free_slots[i] = i
    xorq    %rcx, %rcx
.L_snew_fill:
    movl    %ecx, 40(%rax, %rcx, 4)
    incq    %rcx
    cmpq    %rbx, %rcx
    jb      .L_snew_fill
    popq    %r12
    popq    %rbx
    ret
.L_snew_fail:
    leaq    .L_scope_oom(%rip), %rdi
    movq    $29, %rsi
    call    _fc_rt_panic

# _fc_rt_scope_acquire(%rdi = scope, %rsi = ctx_state, %rdx = ctx_deadline,
#                      %rcx = pool_ptr, %r8 = pool_len, %r9 = elem_size)
# -> (%rax = slot_idx, %rdx = buf_ptr, %rcx = err)
.global _fc_rt_scope_acquire
_fc_rt_scope_acquire:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    subq    $32, %rsp         # timespec[16] + elem_size[8]
    movq    %rdi, %rbx        # scope
    movq    %rsi, %r12        # ctx_state
    movq    %rdx, %r13        # ctx_deadline
    movq    %rcx, %r14        # pool_ptr
    movq    %r8,  %r15        # pool_len
    movq    %r9,  16(%rsp)    # elem_size
.L_sa_loop:
    movq    %r12, %rdi
    movq    %r13, %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_sa_err
    movq    %rbx, %rdi
    call    .L_scope_lock
    movl    24(%rbx), %eax    # free_count
    testl   %eax, %eax
    jnz     .L_sa_pop
    movq    %rbx, %rdi
    call    .L_scope_unlock
    # Wait up to 2ms on futex(&free_count, 0) so cancellation is prompt
    movq    $0, 0(%rsp)
    movq    $2000000, 8(%rsp)
    movq    $SYS_futex, %rax
    leaq    24(%rbx), %rdi
    movq    $FUTEX_WAIT_PRIVATE, %rsi
    xorq    %rdx, %rdx
    movq    %rsp, %r10
    xorq    %r8, %r8
    xorq    %r9, %r9
    syscall
    jmp     .L_sa_loop
.L_sa_pop:
    decl    %eax
    movl    %eax, 24(%rbx)
    movl    40(%rbx, %rax, 4), %edx  # slot_idx
    pushq   %rdx
    movq    %rbx, %rdi
    call    .L_scope_unlock
    popq    %rax              # slot_idx (u64)
    cmpq    %r15, %rax
    jae     .L_sa_pool_small
    movq    16(%rsp), %rcx    # elem_size
    imulq   %rax, %rcx
    leaq    (%r14, %rcx), %rdx # buf_ptr = pool_ptr + slot_idx * elem_size
    xorq    %rcx, %rcx        # err = nil
    jmp     .L_sa_ret
.L_sa_pool_small:
    movq    %rbx, %rdi
    movq    %rax, %rsi
    call    _fc_rt_scope_release
    movq    $ERR_ARENAFULL, %rax
.L_sa_err:
    movq    %rax, %rcx
    xorq    %rax, %rax
    xorq    %rdx, %rdx
.L_sa_ret:
    addq    $32, %rsp
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_scope_release(%rdi = scope, %rsi = slot_idx)
.global _fc_rt_scope_release
_fc_rt_scope_release:
    pushq   %rbx
    pushq   %r12
    movq    %rdi, %rbx
    movq    %rsi, %r12
    call    .L_scope_lock
    movl    24(%rbx), %eax
    movl    %r12d, 40(%rbx, %rax, 4)
    incl    %eax
    movl    %eax, 24(%rbx)
    movq    %rbx, %rdi
    call    .L_scope_unlock
    movq    $SYS_futex, %rax
    leaq    24(%rbx), %rdi
    movq    $FUTEX_WAKE_PRIVATE, %rsi
    movq    $1, %rdx
    syscall
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_scope_go(%rdi = scope, %rsi = slot_idx, %rdx = buf_ptr, %rcx = fn_ptr, %r8 = env_ptr, %r9 = env_size)
# Copies env_size bytes of captured values onto slot_idx's dedicated stack before
# clone(2), so captures are 100% race-free with zero heap allocation!
.global _fc_rt_scope_go
_fc_rt_scope_go:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    movq    %rdi, %rbx        # scope
    movq    %rsi, %r12        # slot_idx
    # Increment active atomically before clone
    lock incl 20(%rbx)
    # Compute child stack top = stacks_base + (slot_idx + 1) * SLOT_STACK_SIZE
    leaq    1(%r12), %rax
    imulq   $SLOT_STACK_SIZE, %rax, %rax
    addq    32(%rbx), %rax    # child_top
    # Reserve aligned env copy on child stack if env_size (%r9) > 0
    movq    %r8, %r13         # env_ptr (default if env_size == 0)
    testq   %r9, %r9
    jz      .L_sg_no_env
    movq    %r9, %r10
    addq    $15, %r10
    andq    $-16, %r10
    subq    %r10, %rax
    movq    %rax, %r13        # child's own env_ptr on child stack
    pushq   %rax
    pushq   %rcx
    pushq   %rdx
    movq    %r13, %rdi
    movq    %r8,  %rsi
    movq    %r9,  %rdx
    call    _fc_rt_memcpy
    popq    %rdx
    popq    %rcx
    popq    %rax
.L_sg_no_env:
    # Place trampoline frame on child stack (48 bytes, 16-byte aligned):
    #   +0:  fn_ptr
    #   +8:  env_ptr (pointing at child's stack copy)
    #   +16: buf_ptr
    #   +24: scope
    #   +32: slot_idx
    subq    $48, %rax
    movq    %rcx, 0(%rax)
    movq    %r13, 8(%rax)
    movq    %rdx, 16(%rax)
    movq    %rbx, 24(%rax)
    movq    %r12, 32(%rax)
    # clone(CLONE_THREAD_FLAGS, child_rsp, 0, 0, 0)
    movq    %rax, %rsi        # child stack
    movq    $CLONE_THREAD_FLAGS, %rdi
    xorq    %rdx, %rdx
    xorq    %r10, %r10
    xorq    %r8, %r8
    movq    $SYS_clone, %rax
    syscall
    testq   %rax, %rax
    jz      .L_child_entry
    # Parent (or error if < 0)
    jg      .L_sg_parent_ok
    lock decl 20(%rbx)
.L_sg_parent_ok:
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

.L_child_entry:
    # We are on the new kernel thread's stack! (%rsp points at trampoline frame)
    xorq    %rbp, %rbp
    movq    0(%rsp),  %rax    # fn_ptr
    movq    8(%rsp),  %r12    # env_ptr (FinCore closure register)
    movq    16(%rsp), %rdx    # buf_ptr
    movq    24(%rsp), %rbx    # scope (callee-saved)
    movq    32(%rsp), %r13    # slot_idx (callee-saved)
    addq    $48, %rsp
    # Call fn(buf_ptr) using FinCore uniform stack ABI (16-byte aligned arg slot)
    subq    $16, %rsp
    movq    %rdx, 0(%rsp)
    call    *%rax
    addq    $16, %rsp
    # Release slot
    movq    %rbx, %rdi
    movq    %r13, %rsi
    call    _fc_rt_scope_release
    # Decrement active; if 0, wake waiters
    lock decl 20(%rbx)
    jnz     .L_child_exit
    movq    $SYS_futex, %rax
    leaq    20(%rbx), %rdi
    movq    $FUTEX_WAKE_PRIVATE, %rsi
    movq    $0x7fffffff, %rdx
    syscall
.L_child_exit:
    xorq    %rdi, %rdi
    movq    $SYS_exit, %rax   # exit ONLY this thread!
    syscall

# _fc_rt_scope_wait(%rdi = scope)
.global _fc_rt_scope_wait
_fc_rt_scope_wait:
    testq   %rdi, %rdi
    jz      .L_sw_done
    pushq   %rbx
    movq    %rdi, %rbx
.L_sw_loop:
    movl    20(%rbx), %edx    # cur active
    testl   %edx, %edx
    jz      .L_sw_ok
    movq    $SYS_futex, %rax
    leaq    20(%rbx), %rdi
    movq    $FUTEX_WAIT_PRIVATE, %rsi
    xorq    %r10, %r10        # no timeout
    xorq    %r8, %r8
    xorq    %r9, %r9
    syscall
    jmp     .L_sw_loop
.L_sw_ok:
    popq    %rbx
.L_sw_done:
    ret

# _fc_rt_scope_wait_timeout(%rdi = scope, %rsi = dur_ns) -> %rax (1 = idle, 0 = timed out)
.global _fc_rt_scope_wait_timeout
_fc_rt_scope_wait_timeout:
    testq   %rdi, %rdi
    jz      .L_swt_idle
    pushq   %rbx
    pushq   %r12
    subq    $24, %rsp         # timespec[16]
    movq    %rdi, %rbx
    movq    %rsi, %r12
    call    _fc_rt_clock_now
    addq    %rax, %r12        # deadline = now + dur_ns
.L_swt_loop:
    movl    20(%rbx), %edx
    testl   %edx, %edx
    jz      .L_swt_done_1
    pushq   %rdx
    call    _fc_rt_clock_now
    popq    %rdx
    movq    %r12, %rcx
    subq    %rax, %rcx
    jle     .L_swt_done_0
    # Convert rem_ns (%rcx) to struct timespec (sec, nsec)
    movq    %rcx, %rax
    xorq    %rdx, %rdx
    movq    $1000000000, %r8
    divq    %r8
    movq    %rax, 0(%rsp)
    movq    %rdx, 8(%rsp)
    movl    20(%rbx), %edx
    testl   %edx, %edx
    jz      .L_swt_done_1
    movq    $SYS_futex, %rax
    leaq    20(%rbx), %rdi
    movq    $FUTEX_WAIT_PRIVATE, %rsi
    movq    %rsp, %r10
    xorq    %r8, %r8
    xorq    %r9, %r9
    syscall
    jmp     .L_swt_loop
.L_swt_done_1:
    movq    $1, %rax
    jmp     .L_swt_ret
.L_swt_done_0:
    xorq    %rax, %rax
.L_swt_ret:
    addq    $24, %rsp
    popq    %r12
    popq    %rbx
    ret
.L_swt_idle:
    movq    $1, %rax
    ret

# _fc_rt_scope_destroy(%rdi = scope) — waits then munmaps the bounded scope region!
.weak _fc_rt_fscope_destroy
.weak _fc_rt_fscope_ready
.weak _fc_rt_fscope_spawn
.global _fc_rt_scope_destroy
_fc_rt_scope_destroy:
    testq   %rdi, %rdi
    jz      .L_sd_done
    cmpl    $1, 28(%rdi)      # FiberScope?
    jne     .L_sd_thread
    leaq    _fc_rt_fscope_destroy(%rip), %rax
    testq   %rax, %rax
    jz      .L_sd_done
    jmp     *%rax
.L_sd_thread:
    pushq   %rbx
    movq    %rdi, %rbx
    call    _fc_rt_scope_wait
    movq    8(%rbx), %rsi     # total_mmap_size
    movq    %rbx, %rdi
    movq    $SYS_munmap, %rax
    syscall
    popq    %rbx
.L_sd_done:
    ret

# _fc_rt_scope_ready(%rdi = scope, %rsi = ctx_state, %rdx = ctx_deadline) -> %rax = err
.global _fc_rt_scope_ready
_fc_rt_scope_ready:
    testq   %rdi, %rdi
    jz      .L_sr_ok
    cmpl    $1, 28(%rdi)
    jne     .L_sr_thread
    leaq    _fc_rt_fscope_ready(%rip), %rax
    testq   %rax, %rax
    jz      .L_sr_ok
    jmp     *%rax
.L_sr_thread:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    subq    $24, %rsp
    movq    %rdi, %rbx
    movq    %rsi, %r12
    movq    %rdx, %r13
.L_sr_loop:
    movq    %r12, %rdi
    movq    %r13, %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_sr_ret
    movl    24(%rbx), %eax
    testl   %eax, %eax
    jnz     .L_sr_th_ok
    movq    $0, 0(%rsp)
    movq    $5000000, 8(%rsp)
    movq    $SYS_futex, %rax
    leaq    24(%rbx), %rdi
    movq    $FUTEX_WAIT_PRIVATE, %rsi
    xorq    %rdx, %rdx
    movq    %rsp, %r10
    xorq    %r8, %r8
    xorq    %r9, %r9
    syscall
    jmp     .L_sr_loop
.L_sr_th_ok:
    xorq    %rax, %rax
.L_sr_ret:
    addq    $24, %rsp
    popq    %r13
    popq    %r12
    popq    %rbx
    ret
.L_sr_ok:
    xorq    %rax, %rax
    ret

# _fc_rt_scope_spawn(%rdi = scope, %rsi = ctx_state, %rdx = ctx_deadline,
#                    %rcx = fn_ptr, %r8 = env_ptr, %r9 = env_sz) -> %rax = err
.global _fc_rt_scope_spawn
_fc_rt_scope_spawn:
    testq   %rdi, %rdi
    jz      .L_ssp_ok
    cmpl    $1, 28(%rdi)
    jne     .L_ssp_thread
    leaq    _fc_rt_fscope_spawn(%rip), %rax
    testq   %rax, %rax
    jz      .L_ssp_ok
    jmp     *%rax
.L_ssp_thread:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    subq    $32, %rsp
    movq    %rdi, %rbx          # scope
    movq    %rcx, 0(%rsp)       # fn_ptr
    movq    %r8,  8(%rsp)       # env_ptr
    movq    %r9,  16(%rsp)      # env_sz
    # Acquire a free thread slot (passing pool=0, pool_len=max_tasks, stride=0)
    xorq    %rcx, %rcx
    movq    0(%rbx), %r8
    xorq    %r9, %r9
    call    _fc_rt_scope_acquire
    # returns %rax = slot_idx, %rdx = buf_ptr, %rcx = err
    testq   %rcx, %rcx
    jnz     .L_ssp_err
    movq    %rax, %r12          # slot_idx
    lock incl 20(%rbx)          # active++
    movq    %r12, %rax
    incq    %rax
    imulq   $SLOT_STACK_SIZE, %rax, %rax
    addq    32(%rbx), %rax      # child stack top
    movq    8(%rsp), %r8        # env_ptr
    movq    16(%rsp), %r9       # env_sz
    xorq    %r13, %r13
    testq   %r8, %r8
    jz      .L_ssp_no_env
    testq   %r9, %r9
    jz      .L_ssp_no_env
    movq    %r9, %r10
    addq    $15, %r10
    andq    $-16, %r10
    subq    %r10, %rax
    movq    %rax, %r13
    pushq   %rax
    movq    %r13, %rdi
    movq    %r8, %rsi
    movq    %r9, %rdx
    call    _fc_rt_memcpy
    popq    %rax
.L_ssp_no_env:
    subq    $32, %rax
    movq    0(%rsp), %rcx
    movq    %rcx, 0(%rax)       # fn_ptr
    movq    %r13, 8(%rax)       # env_ptr
    movq    %rbx, 16(%rax)      # scope
    movq    %r12, 24(%rax)      # slot_idx
    movq    %rax, %rsi
    movq    $CLONE_THREAD_FLAGS, %rdi
    xorq    %rdx, %rdx
    xorq    %r10, %r10
    xorq    %r8, %r8
    movq    $SYS_clone, %rax
    syscall
    testq   %rax, %rax
    jz      .L_ssp_child
    jg      .L_ssp_th_ok
    lock decl 20(%rbx)
    movq    %rbx, %rdi
    movq    %r12, %rsi
    call    _fc_rt_scope_release
.L_ssp_th_ok:
    xorq    %rax, %rax
    jmp     .L_ssp_ret
.L_ssp_err:
    movq    %rcx, %rax
.L_ssp_ret:
    addq    $32, %rsp
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    ret
.L_ssp_ok:
    xorq    %rax, %rax
    ret
.L_ssp_child:
    xorq    %rbp, %rbp
    movq    0(%rsp),  %rax      # fn_ptr
    movq    8(%rsp),  %r12      # env_ptr
    movq    16(%rsp), %rbx      # scope
    movq    24(%rsp), %r13      # slot_idx
    addq    $32, %rsp
    call    *%rax
    movq    %rbx, %rdi
    movq    %r13, %rsi
    call    _fc_rt_scope_release
    lock decl 20(%rbx)
    jnz     .L_ssp_cexit
    movq    $SYS_futex, %rax
    leaq    20(%rbx), %rdi
    movq    $FUTEX_WAKE_PRIVATE, %rsi
    movq    $0x7fffffff, %rdx
    syscall
.L_ssp_cexit:
    xorq    %rdi, %rdi
    movq    $SYS_exit, %rax
    syscall

# ---------------------------------------------------------------------------
# Bounded Runtime Arena (FinCore v0.4 — O(1) bump alloc + O(1) munmap, zero GC)
#
# fc_arena header layout (32 bytes, 16-byte aligned data starts at +32):
#   +0:  cap_bytes       (u64)
#   +8:  used_bytes      (u64)
#   +16: total_mmap_size (u64)
#   +24: reserved        (u64)
#   +32: data[cap_bytes]

# _fc_rt_arena_new(%rdi = cap_bytes) -> %rax = arena_ptr
.global _fc_rt_arena_new
_fc_rt_arena_new:
    pushq   %rbx
    movq    %rdi, %rbx          # cap_bytes
    leaq    32(%rbx), %rsi
    addq    $4095, %rsi
    andq    $-4096, %rsi        # total_mmap_size = alignUp(32 + cap, 4096)
    pushq   %rsi
    movq    $SYS_mmap, %rax
    xorq    %rdi, %rdi
    movq    $PROT_RW, %rdx
    movq    $MAP_ANON, %r10
    movq    $-1, %r8
    xorq    %r9, %r9
    syscall
    popq    %rsi
    cmpq    $-4095, %rax
    jae     .L_anew_fail
    movq    %rbx, 0(%rax)       # cap_bytes
    movq    $0,   8(%rax)       # used_bytes
    movq    %rsi, 16(%rax)      # total_mmap_size
    movq    $0,   24(%rax)
    popq    %rbx
    ret
.L_anew_fail:
    leaq    .L_arena_oom(%rip), %rdi
    movq    $25, %rsi
    call    _fc_rt_panic

# _fc_rt_arena_alloc_bytes(%rdi = arena, %rsi = nbytes) -> (%rax = ptr, %rdx = len, %rcx = err)
.global _fc_rt_arena_alloc_bytes
_fc_rt_arena_alloc_bytes:
    testq   %rdi, %rdi
    jz      .L_aa_full
    cmpq    0(%rdi), %rsi       # overflow guard: nbytes > cap_bytes?
    ja      .L_aa_full
    # aligned_n = alignUp(nbytes, 8)
    movq    %rsi, %r8
    addq    $7, %r8
    andq    $-8, %r8
    movq    8(%rdi), %r9        # cur used
    leaq    (%r9, %r8), %r10    # new used
    cmpq    0(%rdi), %r10       # new used > cap_bytes?
    ja      .L_aa_full
    movq    %r10, 8(%rdi)
    leaq    32(%rdi, %r9), %rax # ptr = arena + 32 + old_used
    movq    %rsi, %rdx          # len = requested nbytes
    xorq    %rcx, %rcx          # err = nil
    ret
.L_aa_full:
    xorq    %rax, %rax
    xorq    %rdx, %rdx
    movq    $ERR_ARENAFULL, %rcx
    ret

# _fc_rt_arena_used(%rdi = arena) -> %rax
.global _fc_rt_arena_used
_fc_rt_arena_used:
    testq   %rdi, %rdi
    jz      .L_au_zero
    movq    8(%rdi), %rax
    ret
.L_au_zero:
    xorq    %rax, %rax
    ret

# _fc_rt_arena_cap(%rdi = arena) -> %rax
.global _fc_rt_arena_cap
_fc_rt_arena_cap:
    testq   %rdi, %rdi
    jz      .L_ac_zero
    movq    0(%rdi), %rax
    ret
.L_ac_zero:
    xorq    %rax, %rax
    ret

# _fc_rt_arena_reset(%rdi = arena)
.global _fc_rt_arena_reset
_fc_rt_arena_reset:
    testq   %rdi, %rdi
    jz      .L_ar_done
    movq    8(%rdi), %rcx
    testq   %rcx, %rcx
    jz      .L_ar_done
    movq    $0, 8(%rdi)
    leaq    32(%rdi), %rdi
    xorl    %eax, %eax
    cld
    rep stosb
.L_ar_done:
    ret

# _fc_rt_arena_destroy(%rdi = arena)
.global _fc_rt_arena_destroy
_fc_rt_arena_destroy:
    testq   %rdi, %rdi
    jz      .L_ad_done
    movq    16(%rdi), %rsi      # total_mmap_size
    movq    $SYS_munmap, %rax
    syscall
.L_ad_done:
    ret

.section .rodata
.L_scope_oom: .ascii "panic: scope mmap failed\n"
.L_arena_oom: .ascii "panic: arena mmap failed\n"
