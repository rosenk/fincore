# runtime/linux_amd64/fiber.s — zero-libc, zero-GC M:N epoll Fiber Scheduler
# ("FinCore Eio"): cooperative fibers multiplexed onto a fixed pool of Linux OS
# worker threads via epoll(7) + eventfd(2) + futex(2) + %fs TLS.
#
# All fiber stacks (32 KiB each), fiber descriptors, ready ring, and worker
# stacks live in ONE contiguous mmap(2) region (demand-paged by Linux, freed in
# O(1) via munmap(2) on scope exit). Zero malloc, zero GC, zero libc.

.text

.equ SYS_read,          0
.equ SYS_write,         1
.equ SYS_close,         3
.equ SYS_mmap,          9
.equ SYS_munmap,        11
.equ SYS_clone,         56
.equ SYS_exit,          60
.equ SYS_arch_prctl,    158
.equ SYS_futex,         202
.equ SYS_epoll_wait,    232
.equ SYS_epoll_ctl,     233
.equ SYS_eventfd2,      290
.equ SYS_epoll_create1, 291

.equ ARCH_SET_FS,        0x1002
.equ PROT_RW,            3
.equ MAP_ANON,           0x22
.equ CLONE_THREAD_FLAGS, 0x00010f00
.equ FUTEX_WAIT_PRIVATE, 128
.equ FUTEX_WAKE_PRIVATE, 129

.equ EPOLL_CLOEXEC,      0x80000
.equ EFD_NONBLOCK,       0x800
.equ EFD_CLOEXEC,        0x80000
.equ EPOLL_CTL_ADD,      1
.equ EPOLL_CTL_DEL,      2
.equ EPOLL_CTL_MOD,      3
.equ EPOLLIN,            0x001
.equ EPOLLOUT,           0x004
.equ EPOLLONESHOT,       0x40000000

.equ EINTR,              4
.equ EEXIST,             17
.equ ERR_NIL,            0
.equ ERR_TIMEOUT,        2
.equ ERR_CANCELLED,      3
.equ ERR_IO,             6

.equ FIBER_STACK_SIZE,   32768
.equ WORKER_STACK_SIZE,  65536
.equ FIBER_DESC_SIZE,    128
.equ WORKER_TLS_SIZE,    64
.equ FSCOPE_HDR_SIZE,    128
.equ MAX_REG_FSCOPES,    32

.equ FIBER_STATE_FREE,   0
.equ FIBER_STATE_READY,  1
.equ FIBER_STATE_RUN,    2
.equ FIBER_STATE_WAIT,   3
.equ FIBER_STATE_DONE,   4

# FiberScope header offsets (compatible with fc_scope at +0..+28):
#   +0:   max_fibers      (u64)
#   +8:   total_mmap_size (u64)
#   +16:  lock            (u32 spinlock)
#   +20:  active_fibers   (u32, futex word)
#   +24:  free_count      (u32, futex word)
#   +28:  scope_kind      (u32 = 1 for FiberScope)
#   +32:  num_workers     (u32)
#   +36:  alive_workers   (u32, futex word)
#   +40:  epoll_fd        (i32)
#   +44:  wake_efd        (i32)
#   +48:  shutdown        (u32)
#   +52:  poller_busy     (u32)
#   +56:  work_seq        (u32, futex word)
#   +60:  ready_count     (u32)
#   +64:  ready_head      (u32)
#   +68:  ready_tail      (u32)
#   +72:  fibers_ptr      (u64)
#   +80:  free_slots_ptr  (u64)
#   +88:  ready_ring_ptr  (u64)
#   +96:  stacks_base     (u64)
#   +104: workers_base    (u64)
#   +112: reg_idx         (i32)
#   +116: wait_count      (u32)
#   +120: high_water      (u32)

# Fiber descriptor offsets (128 bytes):
#   +0:   saved_rsp       (u64)
#   +8:   fscope          (u64)
#   +16:  stack_top       (u64)
#   +24:  fn_ptr          (u64)
#   +32:  env_ptr         (u64)
#   +40:  state           (u32)
#   +44:  slot_idx        (u32)
#   +48:  wait_fd         (i32)
#   +52:  wait_events     (u32)
#   +56:  wait_ctx_state  (u64)
#   +64:  wait_deadline   (u64)
#   +72:  wake_err        (u64)
#   +80:  in_epoll        (u32)
#   +84:  wait_kind       (u32: 0=yield, 1=io, 2=sleep)

# Worker TLS offsets (pointed to by %fs):
#   %fs:0:  cur_fiber     (u64)
#   %fs:8:  fscope        (u64)
#   %fs:16: sched_rsp     (u64)
#   %fs:24: worker_idx    (u64)

# Internal spinlock on 16(%rdi)
.L_fs_lock:
    movl    $1, %eax
    xchgl   %eax, 16(%rdi)
    testl   %eax, %eax
    jz      .L_fs_lock_ok
    pause
    jmp     .L_fs_lock
.L_fs_lock_ok:
    ret

.L_fs_unlock:
    movl    $0, 16(%rdi)
    ret

# Internal: wake epoll eventfd of fscope (%rdi = fscope)
.L_fs_wake_efd:
    movl    44(%rdi), %edi
    cmpl    $0, %edi
    jl      .L_fwe_done
    subq    $16, %rsp
    movq    $1, 0(%rsp)
    movq    %rsp, %rsi
    movq    $8, %rdx
    movq    $SYS_write, %rax
    syscall
    addq    $16, %rsp
.L_fwe_done:
    ret

# _fc_rt_fiber_wake_all() — async-signal-safe wakeup of all active FiberScopes
# Called from _fc_rt_ctx_cancel_state when any Context is cancelled!
.global _fc_rt_fiber_wake_all
_fc_rt_fiber_wake_all:
    pushq   %rbx
    pushq   %rcx
    subq    $8, %rsp
    movq    $1, 0(%rsp)
    leaq    _fc_fscope_reg(%rip), %rbx
    xorq    %rcx, %rcx
.L_fwa_loop:
    cmpq    $MAX_REG_FSCOPES, %rcx
    jae     .L_fwa_done
    movq    (%rbx, %rcx, 8), %rax
    testq   %rax, %rax
    jz      .L_fwa_next
    movl    44(%rax), %edi      # wake_efd
    cmpl    $0, %edi
    jl      .L_fwa_next
    pushq   %rcx
    movq    $SYS_write, %rax
    leaq    8(%rsp), %rsi
    movq    $8, %rdx
    syscall
    popq    %rcx
.L_fwa_next:
    incq    %rcx
    jmp     .L_fwa_loop
.L_fwa_done:
    addq    $8, %rsp
    popq    %rcx
    popq    %rbx
    ret

# _fc_rt_fscope_new(%rdi = workers, %rsi = max_fibers) -> %rax = fscope
.global _fc_rt_fscope_new
_fc_rt_fscope_new:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    # Clamp workers to [1, 32] (0 defaults to 4)
    testq   %rdi, %rdi
    jnz     .L_fn_w_nz
    movq    $4, %rdi
.L_fn_w_nz:
    cmpq    $32, %rdi
    jbe     .L_fn_w_ok
    movq    $32, %rdi
.L_fn_w_ok:
    movq    %rdi, %r12          # r12 = num_workers
    testq   %rsi, %rsi
    jnz     .L_fn_m_ok
    movq    $1, %rsi
.L_fn_m_ok:
    movq    %rsi, %r13          # r13 = max_fibers

    # Compute contiguous mmap layout:
    #   hdr (128)
    #   fibers:      max_fibers * 128
    #   free_slots:  align16(max_fibers * 4)
    #   ready_ring:  align16(max_fibers * 4)
    #   stacks:      page-aligned, max_fibers * 32768
    #   workers:     num_workers * (64 + 65536)
    movq    $FSCOPE_HDR_SIZE, %rax
    movq    %r13, %rcx
    shlq    $7, %rcx            # max_fibers * 128
    addq    %rcx, %rax          # end of fibers = free_slots_off
    movq    %rax, %r14          # r14 = free_slots_off
    movq    %r13, %rcx
    shlq    $2, %rcx
    addq    $15, %rcx
    andq    $-16, %rcx
    addq    %rcx, %rax          # ready_ring_off
    movq    %rax, %r15          # r15 = ready_ring_off
    addq    %rcx, %rax
    addq    $4095, %rax
    andq    $-4096, %rax        # stacks_off (page-aligned!)
    pushq   %rax                # [rsp+8] = stacks_off
    movq    %r13, %rcx
    shlq    $15, %rcx           # max_fibers * 32768
    addq    %rcx, %rax          # workers_off
    pushq   %rax                # [rsp+0] = workers_off
    movq    %r12, %rcx
    imulq   $(WORKER_TLS_SIZE + WORKER_STACK_SIZE), %rcx, %rcx
    addq    %rcx, %rax
    addq    $4095, %rax
    andq    $-4096, %rax        # total_mmap_size
    movq    %rax, %rbx          # rbx = total_mmap_size

    # mmap(NULL, total_mmap_size, PROT_RW, MAP_ANON, -1, 0)
    movq    $SYS_mmap, %rax
    xorq    %rdi, %rdi
    movq    %rbx, %rsi
    movq    $PROT_RW, %rdx
    movq    $MAP_ANON, %r10
    movq    $-1, %r8
    xorq    %r9, %r9
    syscall
    cmpq    $-4095, %rax
    jae     .L_fn_oom
    movq    %rax, %rsi          # rsi = fscope base
    popq    %rcx                # workers_off
    popq    %rdx                # stacks_off

    # Fill header
    movq    %r13, 0(%rsi)       # max_fibers
    movq    %rbx, 8(%rsi)       # total_mmap_size
    movl    $0,   16(%rsi)      # lock
    movl    $0,   20(%rsi)      # active_fibers
    movl    %r13d, 24(%rsi)     # free_count = max_fibers
    movl    $1,   28(%rsi)      # scope_kind = 1 (FiberScope!)
    movl    %r12d, 32(%rsi)     # num_workers
    movl    %r12d, 36(%rsi)     # alive_workers
    movl    $-1,  40(%rsi)      # epoll_fd
    movl    $-1,  44(%rsi)      # wake_efd
    movl    $0,   48(%rsi)      # shutdown
    movl    $0,   52(%rsi)      # poller_busy
    movl    $0,   56(%rsi)      # work_seq
    movl    $0,   60(%rsi)      # ready_count
    movl    $0,   64(%rsi)      # ready_head
    movl    $0,   68(%rsi)      # ready_tail
    leaq    FSCOPE_HDR_SIZE(%rsi), %rax
    movq    %rax, 72(%rsi)      # fibers_ptr
    leaq    (%rsi, %r14), %rax
    movq    %rax, 80(%rsi)      # free_slots_ptr
    leaq    (%rsi, %r15), %rax
    movq    %rax, 88(%rsi)      # ready_ring_ptr
    leaq    (%rsi, %rdx), %rax
    movq    %rax, 96(%rsi)      # stacks_base
    leaq    (%rsi, %rcx), %rax
    movq    %rax, 104(%rsi)     # workers_base
    movl    $-1,  112(%rsi)     # reg_idx
    movl    $0,   116(%rsi)     # wait_count
    movl    $0,   120(%rsi)     # high_water

    movq    %rsi, %rbx          # rbx = fscope

    # Initialize free_slots[i] = max_fibers - 1 - i (so slot 0 popped first!)
    # and fibers[i].fscope, .slot_idx, .stack_top, .wait_fd = -1
    movq    80(%rbx), %r8       # free_slots
    movq    72(%rbx), %r9       # fibers
    movq    96(%rbx), %r10      # stacks_base
    xorq    %rcx, %rcx
.L_fn_init:
    cmpq    %r13, %rcx
    jae     .L_fn_init_done
    movq    %r13, %rax
    decq    %rax
    subq    %rcx, %rax
    movl    %eax, (%r8, %rcx, 4)
    movq    %rcx, %rax
    shlq    $7, %rax            # i * 128
    leaq    (%r9, %rax), %r11   # &fibers[i]
    movq    %rbx, 8(%r11)       # fscope
    movl    %ecx, 44(%r11)      # slot_idx
    movq    %rcx, %rdx
    incq    %rdx
    shlq    $15, %rdx           # (i+1) * 32768
    addq    %r10, %rdx
    movq    %rdx, 16(%r11)      # stack_top
    movl    $-1,  48(%r11)      # wait_fd = -1
    incq    %rcx
    jmp     .L_fn_init
.L_fn_init_done:

    # epoll_create1(EPOLL_CLOEXEC)
    movq    $SYS_epoll_create1, %rax
    movq    $EPOLL_CLOEXEC, %rdi
    syscall
    testq   %rax, %rax
    jl      .L_fn_oom
    movl    %eax, 40(%rbx)      # epoll_fd

    # eventfd2(0, EFD_NONBLOCK | EFD_CLOEXEC)
    movq    $SYS_eventfd2, %rax
    xorq    %rdi, %rdi
    movq    $(EFD_NONBLOCK | EFD_CLOEXEC), %rsi
    syscall
    testq   %rax, %rax
    jl      .L_fn_oom
    movl    %eax, 44(%rbx)      # wake_efd

    # Register wake_efd in epoll_fd with data.u64 = 0
    subq    $16, %rsp
    movl    $EPOLLIN, 0(%rsp)
    movq    $0, 4(%rsp)
    movq    $SYS_epoll_ctl, %rax
    movl    40(%rbx), %edi
    movq    $EPOLL_CTL_ADD, %rsi
    movl    44(%rbx), %edx
    movq    %rsp, %r10
    syscall
    addq    $16, %rsp

    # Register fscope in global _fc_fscope_reg[32]
    leaq    _fc_fscope_reg(%rip), %r8
    xorq    %rcx, %rcx
.L_fn_reg:
    cmpq    $MAX_REG_FSCOPES, %rcx
    jae     .L_fn_reg_done
    xorq    %rax, %rax
    lock cmpxchgq %rbx, (%r8, %rcx, 8)
    jz      .L_fn_reg_hit
    incq    %rcx
    jmp     .L_fn_reg
.L_fn_reg_hit:
    movl    %ecx, 112(%rbx)
.L_fn_reg_done:

    # Spawn num_workers OS worker threads
    movq    104(%rbx), %r14     # workers_base
    xorq    %r15, %r15          # worker_idx = 0
.L_fn_spawn_w:
    cmpq    %r12, %r15
    jae     .L_fn_done
    # Layout of worker i: [tls (64 bytes)][stack (WORKER_STACK_SIZE)]
    movq    %r15, %rax
    imulq   $(WORKER_TLS_SIZE + WORKER_STACK_SIZE), %rax, %rax
    leaq    (%r14, %rax), %r8   # r8 = &worker_tls
    movq    $0,    0(%r8)       # cur_fiber = 0
    movq    %rbx,  8(%r8)       # fscope
    movq    $0,   16(%r8)       # sched_rsp = 0
    movq    %r15, 24(%r8)       # worker_idx
    leaq    (WORKER_TLS_SIZE + WORKER_STACK_SIZE)(%r8), %rsi # child stack top
    subq    $16, %rsi
    movq    %r8, 0(%rsi)        # pass worker_tls ptr on child stack
    movq    $CLONE_THREAD_FLAGS, %rdi
    xorq    %rdx, %rdx
    xorq    %r10, %r10
    xorq    %r8, %r8
    xorq    %r9, %r9
    movq    $SYS_clone, %rax
    syscall
    testq   %rax, %rax
    jz      .L_worker_entry
    jg      .L_fn_w_next
    lock decl 36(%rbx)          # clone failed -> decrement alive_workers
.L_fn_w_next:
    incq    %r15
    jmp     .L_fn_spawn_w

.L_fn_done:
    movq    %rbx, %rax
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    ret
.L_fn_oom:
    leaq    .L_fscope_oom(%rip), %rdi
    movq    $29, %rsi
    call    _fc_rt_panic

# ---------------------------------------------------------------------------
# Worker OS Thread Entry & M:N Scheduler Loop
.L_worker_entry:
    xorq    %rbp, %rbp
    movq    0(%rsp), %r12       # worker_tls
    addq    $16, %rsp
    # Set this worker thread's %fs base to worker_tls
    movq    $SYS_arch_prctl, %rax
    movq    $ARCH_SET_FS, %rdi
    movq    %r12, %rsi
    syscall
    movq    $1, _fc_fiber_runtime_active(%rip)
    movq    8(%r12), %rbx       # rbx = fscope (callee-saved in worker loop)
    subq    $800, %rsp          # epoll_events[64] (64 * 12 = 768 bytes) + scratch

.L_w_loop:
    # 1. Try to pop a ready fiber under lock
    movq    %rbx, %rdi
    call    .L_fs_lock
    movl    60(%rbx), %eax      # ready_count
    testl   %eax, %eax
    jz      .L_w_no_ready
    decl    %eax
    movl    %eax, 60(%rbx)
    movl    64(%rbx), %ecx      # ready_head
    movq    88(%rbx), %rdx      # ready_ring
    movl    (%rdx, %rcx, 4), %r13d  # r13 = slot_idx
    incl    %ecx
    cmpl    0(%rbx), %ecx       # wrap around max_fibers
    jb      .L_w_head_ok
    xorl    %ecx, %ecx
.L_w_head_ok:
    movl    %ecx, 64(%rbx)
    movq    %rbx, %rdi
    call    .L_fs_unlock

    # 2. Switch into fiber r13!
    movq    %r13, %rax
    shlq    $7, %rax
    addq    72(%rbx), %rax
    movq    %rax, %r14          # r14 = *Fiber
    movq    %r14, %rdi
    call    .L_fiber_switch_in

    # 3. Fiber returned to scheduler! Handle DONE vs WAIT/YIELD safely on worker stack.
    movl    40(%r14), %eax      # fiber->state
    cmpl    $FIBER_STATE_DONE, %eax
    je      .L_w_fiber_done

    # Fiber parked (yield, io, or sleep)
    movl    84(%r14), %eax      # wait_kind
    testl   %eax, %eax
    jz      .L_w_requeue_ready  # cooperative yield -> push straight to ready ring

    # Check if ctx is already cancelled / deadline already expired BEFORE parking!
    movq    56(%r14), %rdi      # wait_ctx_state
    movq    64(%r14), %rsi      # wait_deadline
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_w_park_immediate_err

    # Commit WAIT state under lock and arm epoll_ctl (if wait_fd >= 0)
    movq    %rbx, %rdi
    call    .L_fs_lock
    movl    $FIBER_STATE_WAIT, 40(%r14)
    incl    116(%rbx)           # wait_count++
    movl    48(%r14), %edx      # wait_fd
    cmpl    $0, %edx
    jl      .L_w_park_unlock
    # epoll_ctl(epoll_fd, EPOLL_CTL_ADD, wait_fd, {wait_events|EPOLLONESHOT, slot_idx+1})
    movl    52(%r14), %eax
    orl     $EPOLLONESHOT, %eax
    movl    %eax, 768(%rsp)     # struct epoll_event at 768(%rsp)
    movq    %r13, %rax
    incq    %rax                # tag = slot_idx + 1
    movq    %rax, 772(%rsp)
    movl    40(%rbx), %edi      # epoll_fd
    movq    $EPOLL_CTL_ADD, %rsi
    leaq    768(%rsp), %r10
    movq    $SYS_epoll_ctl, %rax
    syscall
    cmpq    $-EEXIST, %rax
    jne     .L_w_park_ep_ok
    movl    40(%rbx), %edi
    movq    $EPOLL_CTL_MOD, %rsi
    movl    48(%r14), %edx
    leaq    768(%rsp), %r10
    movq    $SYS_epoll_ctl, %rax
    syscall
.L_w_park_ep_ok:
    movl    $1, 80(%r14)        # in_epoll = 1
.L_w_park_unlock:
    # Check if ctx got cancelled right as we registered!
    movq    %rbx, %rdi
    call    .L_fs_unlock
    movq    56(%r14), %rdi
    movq    64(%r14), %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jz      .L_w_loop
    # Wake poller to collect cancelled fiber immediately
    movq    %rbx, %rdi
    call    .L_fs_wake_efd
    jmp     .L_w_loop

.L_w_park_immediate_err:
    movq    %rax, 72(%r14)      # wake_err = err
.L_w_requeue_ready:
    movq    %rbx, %rdi
    call    .L_fs_lock
    movl    $FIBER_STATE_READY, 40(%r14)
    movl    68(%rbx), %ecx      # ready_tail
    movq    88(%rbx), %rdx
    movl    %r13d, (%rdx, %rcx, 4)
    incl    %ecx
    cmpl    0(%rbx), %ecx
    jb      .L_w_rq_ok
    xorl    %ecx, %ecx
.L_w_rq_ok:
    movl    %ecx, 68(%rbx)
    incl    60(%rbx)            # ready_count++
    incl    56(%rbx)            # work_seq++
    movq    %rbx, %rdi
    call    .L_fs_unlock
    jmp     .L_w_loop

.L_w_fiber_done:
    # Remove wait_fd from epoll if still attached, return slot to free_slots
    movq    %rbx, %rdi
    call    .L_fs_lock
    movl    $FIBER_STATE_FREE, 40(%r14)
    movl    $-1, 48(%r14)
    movl    $0,  80(%r14)
    movl    24(%rbx), %ecx      # free_count
    movq    80(%rbx), %rdx      # free_slots
    movl    %r13d, (%rdx, %rcx, 4)
    incl    %ecx
    movl    %ecx, 24(%rbx)
    movq    %rbx, %rdi
    call    .L_fs_unlock
    # Wake anyone blocked in Ready/Spawn waiting for a free slot
    movq    $SYS_futex, %rax
    leaq    24(%rbx), %rdi
    movq    $FUTEX_WAKE_PRIVATE, %rsi
    movq    $1, %rdx
    syscall
    # Decrement active_fibers; if 0, wake Scope.Wait / Scope.WaitTimeout
    lock decl 20(%rbx)
    jnz     .L_w_loop
    movq    $SYS_futex, %rax
    leaq    20(%rbx), %rdi
    movq    $FUTEX_WAKE_PRIVATE, %rsi
    movq    $0x7fffffff, %rdx
    syscall
    jmp     .L_w_loop

.L_w_no_ready:
    # Still holding lock! Check shutdown && active_fibers == 0
    movl    48(%rbx), %eax      # shutdown
    testl   %eax, %eax
    jz      .L_w_try_poll
    movl    20(%rbx), %eax      # active_fibers
    testl   %eax, %eax
    jz      .L_w_exit_unlock

.L_w_try_poll:
    # Snapshot work_seq before releasing lock
    movl    56(%rbx), %r15d
    # Try to become the poller
    movl    $1, %eax
    xchgl   %eax, 52(%rbx)
    movq    %rbx, %rdi
    pushq   %rax                # save old poller_busy
    call    .L_fs_unlock
    popq    %rax
    testl   %eax, %eax
    jnz     .L_w_sleep_worker   # another worker is already polling!

    # --- I AM THE POLLER ---
    # Scan waiting fibers for cancelled ctx or expired deadlines; find min_deadline
    movq    %rbx, %rdi
    call    .L_fs_lock
    xorq    %r13, %r13          # r13 = min_deadline (0 = none)
    movl    116(%rbx), %eax     # wait_count
    testl   %eax, %eax
    jz      .L_p_scan_done
    call    _fc_rt_clock_now
    movq    %rax, %r14          # r14 = now_ns
    movl    120(%rbx), %r12d    # high_water
    xorq    %rcx, %rcx
.L_p_scan:
    cmpq    %r12, %rcx
    jae     .L_p_scan_done
    movq    %rcx, %rax
    shlq    $7, %rax
    addq    72(%rbx), %rax      # rax = &fibers[rcx]
    cmpl    $FIBER_STATE_WAIT, 40(%rax)
    jne     .L_p_scan_next
    # Check ctx cancelled
    movq    56(%rax), %rdi      # wait_ctx_state
    testq   %rdi, %rdi
    jz      .L_p_chk_dl
    cmpl    $0, 0(%rdi)
    jz      .L_p_chk_dl
    # Cancelled!
    movq    $ERR_CANCELLED, %rdx
    jmp     .L_p_wake_scanned
.L_p_chk_dl:
    movq    64(%rax), %rsi      # wait_deadline
    testq   %rsi, %rsi
    jz      .L_p_scan_next
    cmpq    %rsi, %r14          # now >= deadline?
    jb      .L_p_upd_min
    movq    $ERR_TIMEOUT, %rdx
    jmp     .L_p_wake_scanned
.L_p_upd_min:
    testq   %r13, %r13
    jz      .L_p_set_min
    cmpq    %r13, %rsi
    jae     .L_p_scan_next
.L_p_set_min:
    movq    %rsi, %r13
    jmp     .L_p_scan_next
.L_p_wake_scanned:
    movq    %rdx, 72(%rax)      # wake_err
    movl    $FIBER_STATE_READY, 40(%rax)
    decl    116(%rbx)           # wait_count--
    # Delete fd from epoll if registered
    cmpl    $0, 80(%rax)
    je      .L_p_ws_push
    movl    $0, 80(%rax)
    pushq   %rcx
    pushq   %rax
    movl    40(%rbx), %edi
    movq    $EPOLL_CTL_DEL, %rsi
    movl    48(%rax), %edx
    xorq    %r10, %r10
    movq    $SYS_epoll_ctl, %rax
    syscall
    popq    %rax
    popq    %rcx
.L_p_ws_push:
    movl    68(%rbx), %edi      # ready_tail
    movq    88(%rbx), %rsi
    movl    %ecx, (%rsi, %rdi, 4)
    incl    %edi
    cmpl    0(%rbx), %edi
    jb      .L_p_ws_ok
    xorl    %edi, %edi
.L_p_ws_ok:
    movl    %edi, 68(%rbx)
    incl    60(%rbx)
    incl    56(%rbx)
.L_p_scan_next:
    incq    %rcx
    jmp     .L_p_scan

.L_p_scan_done:
    movl    60(%rbx), %r12d     # ready_count
    movl    48(%rbx), %r15d     # shutdown
    movq    %rbx, %rdi
    call    .L_fs_unlock
    testl   %r12d, %r12d
    jnz     .L_p_release_wake
    testl   %r15d, %r15d
    jnz     .L_p_release_only

    # Compute epoll_wait timeout_ms in %r10
    movq    $-1, %r10
    testq   %r13, %r13
    jz      .L_p_do_epoll
    call    _fc_rt_clock_now
    movq    %r13, %rcx
    subq    %rax, %rcx
    jle     .L_p_timeout_zero
    leaq    999999(%rcx), %rax
    xorq    %rdx, %rdx
    movq    $1000000, %r8
    divq    %r8
    cmpq    $50, %rax
    jbe     .L_p_to_ok
    movq    $50, %rax
.L_p_to_ok:
    movq    %rax, %r10
    jmp     .L_p_do_epoll
.L_p_timeout_zero:
    xorq    %r10, %r10

.L_p_do_epoll:
    movq    $SYS_epoll_wait, %rax
    movl    40(%rbx), %edi      # epoll_fd
    movq    %rsp, %rsi          # events[64] (768 bytes)
    movq    $64, %rdx
    syscall
    testq   %rax, %rax
    jle     .L_p_release_only
    movq    %rax, %r12          # nevents
    movq    %rbx, %rdi
    call    .L_fs_lock
    xorq    %rcx, %rcx
.L_p_ev_loop:
    cmpq    %r12, %rcx
    jae     .L_p_ev_done
    imulq   $12, %rcx, %rax
    movq    4(%rsp, %rax), %rdx # ev.data.u64 (packed offset +4!)
    testq   %rdx, %rdx
    jnz     .L_p_ev_fiber
    # wake_efd fired: drain 8 bytes
    pushq   %rcx
    movq    $SYS_read, %rax
    movl    44(%rbx), %edi
    leaq    784(%rsp), %rsi
    movq    $8, %rdx
    syscall
    popq    %rcx
    jmp     .L_p_ev_next
.L_p_ev_fiber:
    decq    %rdx                # slot_idx = tag - 1
    cmpq    0(%rbx), %rdx
    jae     .L_p_ev_next
    movq    %rdx, %rax
    shlq    $7, %rax
    addq    72(%rbx), %rax      # &fibers[slot_idx]
    cmpl    $FIBER_STATE_WAIT, 40(%rax)
    jne     .L_p_ev_next
    movl    $FIBER_STATE_READY, 40(%rax)
    movq    $0, 72(%rax)        # wake_err = 0
    decl    116(%rbx)           # wait_count--
    movl    68(%rbx), %edi      # ready_tail
    movq    88(%rbx), %rsi
    movl    %edx, (%rsi, %rdi, 4)
    incl    %edi
    cmpl    0(%rbx), %edi
    jb      .L_p_ev_ok
    xorl    %edi, %edi
.L_p_ev_ok:
    movl    %edi, 68(%rbx)
    incl    60(%rbx)
    incl    56(%rbx)
.L_p_ev_next:
    incq    %rcx
    jmp     .L_p_ev_loop
.L_p_ev_done:
    movl    60(%rbx), %r12d
    movq    %rbx, %rdi
    call    .L_fs_unlock
.L_p_release_wake:
    movl    $0, 52(%rbx)        # poller_busy = 0
    testl   %r12d, %r12d
    jz      .L_w_loop
    movq    $SYS_futex, %rax
    leaq    56(%rbx), %rdi
    movq    $FUTEX_WAKE_PRIVATE, %rsi
    movl    %r12d, %edx
    syscall
    jmp     .L_w_loop
.L_p_release_only:
    movl    $0, 52(%rbx)
    jmp     .L_w_loop

.L_w_sleep_worker:
    movq    $SYS_futex, %rax
    leaq    56(%rbx), %rdi      # &work_seq
    movq    $FUTEX_WAIT_PRIVATE, %rsi
    movl    %r15d, %edx         # expected work_seq
    xorq    %r10, %r10
    xorq    %r8, %r8
    xorq    %r9, %r9
    syscall
    jmp     .L_w_loop

.L_w_exit_unlock:
    movq    %rbx, %rdi
    call    .L_fs_unlock
    lock decl 36(%rbx)          # alive_workers--
    jnz     .L_w_thread_exit
    movq    $SYS_futex, %rax
    leaq    36(%rbx), %rdi
    movq    $FUTEX_WAKE_PRIVATE, %rsi
    movq    $0x7fffffff, %rdx
    syscall
.L_w_thread_exit:
    xorq    %rdi, %rdi
    movq    $SYS_exit, %rax
    syscall

# ---------------------------------------------------------------------------
# Fast O(1) Fiber Context Switch (~5 ns)
# .L_fiber_switch_in(%rdi = *Fiber)
.L_fiber_switch_in:
    pushq   %rbp
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    movq    %rsp, %fs:16        # sched_rsp = worker %rsp
    movq    %rdi, %fs:0         # cur_fiber = fiber
    movl    $FIBER_STATE_RUN, 40(%rdi)
    movq    0(%rdi), %rsp       # %rsp = fiber->saved_rsp
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    popq    %rbp
    ret

# .L_fiber_switch_out() — called from inside running fiber after setting wait fields
.L_fiber_switch_out:
    pushq   %rbp
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    movq    %fs:0, %rax         # cur_fiber
    movq    %rsp, 0(%rax)       # fiber->saved_rsp = %rsp
    movq    $0, %fs:0           # cur_fiber = 0
    movq    %fs:16, %rsp        # %rsp = sched_rsp
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    popq    %rbp
    ret

# Initial entry trampoline on a brand-new fiber stack
.L_fiber_entry:
    xorq    %rbp, %rbp
    movq    %fs:0, %rbx         # cur_fiber
    movq    24(%rbx), %rax      # fn_ptr
    movq    32(%rbx), %r12      # env_ptr (FinCore closure register)
    call    *%rax
    # Fiber finished! Mark DONE and yield back to worker loop
    movq    %fs:0, %rbx
    movl    $FIBER_STATE_DONE, 40(%rbx)
    call    .L_fiber_switch_out
    # Unreachable
    ud2

# ---------------------------------------------------------------------------
# Direct-Style Suspension Hooks (called transparently from net.s & sys.s!)

# _fc_rt_fiber_poll_wait(%rdi = fd, %rsi = poll_events, %rdx = ctx_state, %rcx = ctx_deadline) -> %rax = err
.global _fc_rt_fiber_poll_wait
_fc_rt_fiber_poll_wait:
    movq    %fs:0, %rax
    movl    %edi, 48(%rax)      # wait_fd
    movl    %esi, 52(%rax)      # wait_events (POLLIN=1==EPOLLIN, POLLOUT=4==EPOLLOUT!)
    movq    %rdx, 56(%rax)      # wait_ctx_state
    movq    %rcx, 64(%rax)      # wait_deadline
    movq    $0,   72(%rax)      # wake_err
    movl    $1,   84(%rax)      # wait_kind = 1 (I/O)
    call    .L_fiber_switch_out
    movq    %fs:0, %rax
    movq    72(%rax), %rax      # return wake_err
    ret

# _fc_rt_fiber_sleep(%rdi = ctx_state, %rsi = ctx_deadline, %rdx = dur_ns) -> %rax = err
.global _fc_rt_fiber_sleep
_fc_rt_fiber_sleep:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    movq    %rdi, %rbx          # ctx_state
    movq    %rsi, %r12          # ctx_deadline
    movq    %rdx, %r13          # dur_ns
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_fsl_ret
    testq   %r13, %r13
    jz      .L_fsl_ok
    call    _fc_rt_clock_now
    addq    %r13, %rax          # sleep_dl = now + dur_ns
    # Effective deadline = min(sleep_dl, ctx_deadline) if ctx_deadline != 0
    xorq    %r13, %r13          # r13 = 1 if bounded by ctx_deadline
    testq   %r12, %r12
    jz      .L_fsl_park
    cmpq    %r12, %rax
    jbe     .L_fsl_park
    movq    %r12, %rax
    movq    $1, %r13
.L_fsl_park:
    movq    %fs:0, %rcx
    movl    $-1,  48(%rcx)      # no fd
    movl    $0,   52(%rcx)
    movq    %rbx, 56(%rcx)      # ctx_state
    movq    %rax, 64(%rcx)      # effective deadline
    movq    $0,   72(%rcx)
    movl    $2,   84(%rcx)      # wait_kind = 2 (sleep)
    call    .L_fiber_switch_out
    movq    %fs:0, %rcx
    movq    72(%rcx), %rax      # wake_err
    cmpq    $ERR_TIMEOUT, %rax
    jne     .L_fsl_ret
    # If woken by our own sleep deadline (not ctx_deadline), sleep succeeded (err=0)!
    testq   %r13, %r13
    jnz     .L_fsl_ret
.L_fsl_ok:
    xorq    %rax, %rax
.L_fsl_ret:
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_fiber_yield() — cooperative yield if running inside a fiber
.global _fc_rt_fiber_yield
_fc_rt_fiber_yield:
    cmpq    $0, _fc_fiber_runtime_active(%rip)
    je      .L_fy_noop
    movq    %fs:0, %rax
    testq   %rax, %rax
    jz      .L_fy_noop
    movl    $0, 84(%rax)        # wait_kind = 0 (requeue immediately)
    call    .L_fiber_switch_out
.L_fy_noop:
    ret

# ---------------------------------------------------------------------------
# FiberScope Public Methods (dispatched from scope.s when scope_kind == 1)

# _fc_rt_fscope_ready(%rdi = fscope, %rsi = ctx_state, %rdx = ctx_deadline) -> %rax = err
.global _fc_rt_fscope_ready
_fc_rt_fscope_ready:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    subq    $24, %rsp
    movq    %rdi, %rbx
    movq    %rsi, %r12
    movq    %rdx, %r13
.L_fr_loop:
    movq    %r12, %rdi
    movq    %r13, %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_fr_ret
    movl    24(%rbx), %eax      # free_count
    testl   %eax, %eax
    jnz     .L_fr_ok
    # Wait up to 5ms on free_count == 0
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
    jmp     .L_fr_loop
.L_fr_ok:
    xorq    %rax, %rax
.L_fr_ret:
    addq    $24, %rsp
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_fscope_spawn(%rdi = fscope, %rsi = ctx_state, %rdx = ctx_deadline,
#                     %rcx = fn_ptr, %r8 = env_ptr, %r9 = env_sz) -> %rax = err
.global _fc_rt_fscope_spawn
_fc_rt_fscope_spawn:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    subq    $40, %rsp
    movq    %rdi, %rbx          # fscope
    movq    %rsi, %r12          # ctx_state
    movq    %rdx, %r13          # ctx_deadline
    movq    %rcx, 0(%rsp)       # fn_ptr
    movq    %r8,  8(%rsp)       # env_ptr
    movq    %r9,  16(%rsp)      # env_sz
.L_fsp_try:
    movq    %r12, %rdi
    movq    %r13, %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_fsp_ret
    movq    %rbx, %rdi
    call    .L_fs_lock
    movl    24(%rbx), %ecx      # free_count
    testl   %ecx, %ecx
    jnz     .L_fsp_got
    movq    %rbx, %rdi
    call    .L_fs_unlock
    movq    $0, 24(%rsp)
    movq    $5000000, 32(%rsp)
    movq    $SYS_futex, %rax
    leaq    24(%rbx), %rdi
    movq    $FUTEX_WAIT_PRIVATE, %rsi
    xorq    %rdx, %rdx
    leaq    24(%rsp), %r10
    xorq    %r8, %r8
    xorq    %r9, %r9
    syscall
    jmp     .L_fsp_try

.L_fsp_got:
    decl    %ecx
    movl    %ecx, 24(%rbx)
    movq    80(%rbx), %rdx      # free_slots
    movl    (%rdx, %rcx, 4), %r14d  # slot_idx
    # Update high_water = max(high_water, slot_idx + 1)
    leal    1(%r14d), %eax
    cmpl    120(%rbx), %eax
    jbe     .L_fsp_hw_ok
    movl    %eax, 120(%rbx)
.L_fsp_hw_ok:
    lock incl 20(%rbx)          # active_fibers++
    movq    %rbx, %rdi
    call    .L_fs_unlock

    # Setup fiber descriptor & initial fiber stack
    movq    %r14, %rax
    shlq    $7, %rax
    addq    72(%rbx), %rax
    movq    %rax, %r15          # *Fiber
    movq    16(%r15), %rax      # stack_top (16-byte aligned)
    movq    8(%rsp), %rsi       # env_ptr
    movq    16(%rsp), %rdx      # env_sz
    xorq    %rcx, %rcx          # child env_ptr = 0
    testq   %rsi, %rsi
    jz      .L_fsp_no_env
    testq   %rdx, %rdx
    jz      .L_fsp_no_env
    movq    %rdx, %r8
    addq    $15, %r8
    andq    $-16, %r8
    subq    %r8, %rax
    movq    %rax, %rcx          # child env_ptr on fiber stack
    pushq   %rax
    pushq   %rcx
    movq    %rcx, %rdi
    call    _fc_rt_memcpy
    popq    %rcx
    popq    %rax
.L_fsp_no_env:
    movq    0(%rsp), %rdx       # fn_ptr
    movq    %rdx, 24(%r15)
    movq    %rcx, 32(%r15)      # env_ptr
    movl    $-1,  48(%r15)      # wait_fd = -1
    movl    $0,   80(%r15)      # in_epoll = 0
    # Build initial fiber stack frame: ret addr (.L_fiber_entry) + 6 zero regs (48 bytes)
    # Alignment: %rax is 16-byte aligned; pushing ret addr (8) makes rsp%16==8 on entry to .L_fiber_entry!
    subq    $56, %rax
    leaq    .L_fiber_entry(%rip), %rdx
    movq    %rdx, 48(%rax)      # ret addr popped by ret in .L_fiber_switch_in
    movq    $0,   40(%rax)      # rbp
    movq    $0,   32(%rax)      # rbx
    movq    $0,   24(%rax)      # r12
    movq    $0,   16(%rax)      # r13
    movq    $0,    8(%rax)      # r14
    movq    $0,    0(%rax)      # r15
    movq    %rax, 0(%r15)       # fiber->saved_rsp

    # Enqueue onto ready ring and wake worker / epoll poller
    movq    %rbx, %rdi
    call    .L_fs_lock
    movl    $FIBER_STATE_READY, 40(%r15)
    movl    68(%rbx), %ecx      # ready_tail
    movq    88(%rbx), %rdx
    movl    %r14d, (%rdx, %rcx, 4)
    incl    %ecx
    cmpl    0(%rbx), %ecx
    jb      .L_fsp_t_ok
    xorl    %ecx, %ecx
.L_fsp_t_ok:
    movl    %ecx, 68(%rbx)
    incl    60(%rbx)            # ready_count++
    incl    56(%rbx)            # work_seq++
    movl    52(%rbx), %r12d     # snapshot poller_busy
    movq    %rbx, %rdi
    call    .L_fs_unlock

    # Wake one sleeping worker via futex
    movq    $SYS_futex, %rax
    leaq    56(%rbx), %rdi
    movq    $FUTEX_WAKE_PRIVATE, %rsi
    movq    $1, %rdx
    syscall
    # If a poller is sleeping in epoll_wait, wake it too
    testl   %r12d, %r12d
    jz      .L_fsp_ok
    movq    %rbx, %rdi
    call    .L_fs_wake_efd
.L_fsp_ok:
    xorq    %rax, %rax
.L_fsp_ret:
    addq    $40, %rsp
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_fscope_destroy(%rdi = fscope)
.global _fc_rt_fscope_destroy
_fc_rt_fscope_destroy:
    testq   %rdi, %rdi
    jz      .L_fsd_done
    pushq   %rbx
    movq    %rdi, %rbx
    # 1. Wait for all active fibers to finish
    call    _fc_rt_scope_wait
    # 2. Unregister from global _fc_fscope_reg
    movl    112(%rbx), %eax
    cmpl    $0, %eax
    jl      .L_fsd_unreg_ok
    leaq    _fc_fscope_reg(%rip), %rcx
    movq    $0, (%rcx, %rax, 8)
.L_fsd_unreg_ok:
    # 3. Signal worker threads to exit & wake all of them
    movq    %rbx, %rdi
    call    .L_fs_lock
    movl    $1, 48(%rbx)        # shutdown = 1
    incl    56(%rbx)            # work_seq++
    movq    %rbx, %rdi
    call    .L_fs_unlock
    movq    $SYS_futex, %rax
    leaq    56(%rbx), %rdi
    movq    $FUTEX_WAKE_PRIVATE, %rsi
    movq    $0x7fffffff, %rdx
    syscall
    movq    %rbx, %rdi
    call    .L_fs_wake_efd
    # 4. Wait until alive_workers == 0
.L_fsd_join:
    movl    36(%rbx), %edx
    testl   %edx, %edx
    jz      .L_fsd_joined
    movq    $SYS_futex, %rax
    leaq    36(%rbx), %rdi
    movq    $FUTEX_WAIT_PRIVATE, %rsi
    xorq    %r10, %r10
    xorq    %r8, %r8
    xorq    %r9, %r9
    syscall
    jmp     .L_fsd_join
.L_fsd_joined:
    # 5. Close epoll_fd and wake_efd
    movl    40(%rbx), %edi
    cmpl    $0, %edi
    jl      .L_fsd_c2
    movq    $SYS_close, %rax
    syscall
.L_fsd_c2:
    movl    44(%rbx), %edi
    cmpl    $0, %edi
    jl      .L_fsd_unmap
    movq    $SYS_close, %rax
    syscall
.L_fsd_unmap:
    # 6. O(1) munmap of entire FiberScope
    movq    8(%rbx), %rsi
    movq    %rbx, %rdi
    movq    $SYS_munmap, %rax
    syscall
    popq    %rbx
.L_fsd_done:
    ret

.section .rodata
.L_fscope_oom: .ascii "panic: fscope mmap/fd failed\n"

.section .bss
.balign 8
_fc_fscope_reg: .skip (MAX_REG_FSCOPES * 8)
