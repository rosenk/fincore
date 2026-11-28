# runtime/linux_amd64/effect.s — zero-libc, zero-GC one-shot delimited
# continuation runtime for FinCore v0.3 algebraic effects.
#
# Provides bounded stack switching (64 KiB stacks in a fixed thread-safe pool)
# for non-tail-resumptive and abortive effect handlers, with deterministic
# LIFO defer/scope unwinding on abort.
#
# fc_cont layout (80 bytes, 8-byte aligned on handler's stack frame):
#   +0:  parent_rsp  (u64) — saved handler %rsp
#   +8:  child_rsp   (u64) — saved delimited computation %rsp
#   +16: state       (u64) — 0=running, 1=suspended, 2=done, 3=unwinding
#   +24: op_idx      (u64) — 1-based operation index (0=completed)
#   +32: arg_ptr     (u64) — pointer to op args on child stack
#   +40: ret_ptr     (u64) — pointer to op return slot on child stack
#   +48: stack_base  (u64) — base of acquired bounded child stack
#   +56: stack_slot  (u64) — slot index in bounded pool
#   +64: body_fn     (u64) — body thunk code pointer
#   +72: body_env    (u64) — body thunk closure env (%r12)

.text

.equ CONT_STACK_SIZE,  (64 * 1024)
.equ CONT_POOL_SLOTS,  64

.equ CONT_RUNNING,     0
.equ CONT_SUSPENDED,   1
.equ CONT_DONE,        2
.equ CONT_UNWINDING,   3

# _fc_rt_cont_start(%rdi = cont, %rsi = body_fn, %rdx = body_env) -> %rax = op_idx (0=done, >=1=op)
.global _fc_rt_cont_start
_fc_rt_cont_start:
    pushq   %rbx
    pushq   %rbp
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15

    movq    %rdi, %rbx          # cont
    movq    %rsi, 64(%rbx)      # body_fn
    movq    %rdx, 72(%rbx)      # body_env
    movq    $CONT_RUNNING, 16(%rbx)
    movq    $0, 24(%rbx)
    movq    $0, 32(%rbx)
    movq    $0, 40(%rbx)

    # Acquire a free 64 KiB stack slot from bounded pool (lock-free xchg scan)
    xorq    %rcx, %rcx
    leaq    _fc_cont_pool_used(%rip), %r8
.L_cs_find:
    cmpq    $CONT_POOL_SLOTS, %rcx
    jae     .L_cs_pool_full
    movb    $1, %al
    xchgb   %al, (%r8, %rcx)
    testb   %al, %al
    jz      .L_cs_got
    incq    %rcx
    jmp     .L_cs_find

.L_cs_got:
    movq    %rcx, 56(%rbx)      # stack_slot
    imulq   $CONT_STACK_SIZE, %rcx, %rax
    leaq    _fc_cont_stacks(%rip), %r9
    addq    %r9, %rax
    movq    %rax, 48(%rbx)      # stack_base

    # Save parent %rsp (6 callee-saved regs sit directly at (%rsp))
    movq    %rsp, 0(%rbx)

    # Switch to top of child stack (16-byte aligned)
    addq    $CONT_STACK_SIZE, %rax
    movq    %rax, %rsp

    # Keep %rsp 16-byte aligned and save cont pointer at 0(%rsp)
    subq    $16, %rsp
    movq    %rbx, 0(%rsp)

    # Invoke body_fn() with body_env in %r12
    xorq    %rbp, %rbp
    movq    72(%rbx), %r12
    movq    64(%rbx), %rax
    call    *%rax

    # Body returned (either normally OR after unwinding on abort!)
    movq    0(%rsp), %rbx       # restore cont pointer
    movq    $CONT_DONE, 16(%rbx)
    movq    $0, 24(%rbx)

    # Release bounded stack slot (before switching back, slot memory no longer needed after next insns)
    movq    56(%rbx), %rcx
    leaq    _fc_cont_pool_used(%rip), %r8
    # Switch back to parent stack first so no thread reuses our active stack!
    movq    0(%rbx), %rsp
    movb    $0, (%r8, %rcx)

    xorq    %rax, %rax          # return 0 = DONE
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbp
    popq    %rbx
    ret

.L_cs_pool_full:
    leaq    .L_cont_full_msg(%rip), %rdi
    movq    $36, %rsi
    call    _fc_rt_panic

# _fc_rt_cont_yield(%rdi = cont, %rsi = op_idx, %rdx = arg_ptr, %rcx = ret_ptr) -> %rax (0=resumed, 1=abort)
.global _fc_rt_cont_yield
_fc_rt_cont_yield:
    # If already unwinding, immediately return 1 (abort) without suspending
    cmpq    $CONT_UNWINDING, 16(%rdi)
    je      .L_cy_abort_now

    pushq   %rbx
    pushq   %rbp
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15

    movq    %rsp, 8(%rdi)       # child_rsp
    movq    $CONT_SUSPENDED, 16(%rdi)
    movq    %rsi, 24(%rdi)      # op_idx
    movq    %rdx, 32(%rdi)      # arg_ptr
    movq    %rcx, 40(%rdi)      # ret_ptr

    # Switch back to parent stack and return op_idx
    movq    %rsi, %rax
    movq    0(%rdi), %rsp
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbp
    popq    %rbx
    ret

.L_cy_abort_now:
    movq    $1, %rax
    ret

# _fc_rt_cont_resume(%rdi = cont) -> %rax = next_op_idx (0=done, >=1=op)
.global _fc_rt_cont_resume
_fc_rt_cont_resume:
    pushq   %rbx
    pushq   %rbp
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15

    # Save current parent %rsp and mark running
    movq    %rsp, 0(%rdi)
    movq    $CONT_RUNNING, 16(%rdi)

    # Switch to child stack and return 0 (resumed normally) from _fc_rt_cont_yield
    movq    8(%rdi), %rsp
    xorq    %rax, %rax
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbp
    popq    %rbx
    ret

# _fc_rt_cont_abort(%rdi = cont)
# Unwinds a suspended continuation by resuming it with abort=1 so every active
# defer and scope barrier on the child stack runs in exact LIFO order!
.global _fc_rt_cont_abort
_fc_rt_cont_abort:
    cmpq    $CONT_SUSPENDED, 16(%rdi)
    jne     .L_ca_done

    pushq   %rbx
    pushq   %rbp
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15

    movq    %rsp, 0(%rdi)
    movq    $CONT_UNWINDING, 16(%rdi)

    # Resume child with %rax = 1 (abort); child unwinds all defers/scopes and
    # returns through _fc_rt_cont_start's trampoline back to our parent_rsp!
    movq    8(%rdi), %rsp
    movq    $1, %rax
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbp
    popq    %rbx
    ret

.L_ca_done:
    ret

.section .rodata
.L_cont_full_msg: .ascii "panic: continuation stack pool full\n"

.section .bss
.balign 8
_fc_cont_pool_used: .skip CONT_POOL_SLOTS
.balign 16
_fc_cont_stacks:    .skip (CONT_POOL_SLOTS * CONT_STACK_SIZE)

.section .note.GNU-stack,"",@progbits
