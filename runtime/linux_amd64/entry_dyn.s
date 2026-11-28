# runtime/linux_amd64/entry_dyn.s — C-ABI main() entry point used ONLY when
# linking dynamically against external C/GPU libraries (e.g. -lvulkan -lSDL3).
# Static zero-libc builds never link this file and use _start in sys.s directly.

.text
.global main
main:
    pushq   %rbp
    movq    %rsp, %rbp
    # System V AMD64 main(int argc, char **argv, char **envp)
    movq    %rdi, _fc_rt_argc(%rip)
    movq    %rsi, _fc_rt_argv(%rip)
    movq    %rdx, _fc_rt_envp(%rip)
    call    _fc_entry
    testq   %rax, %rax
    jz      .L_dyn_ok
    cmpq    $0, _fc_rt_stderr_written(%rip)
    jne     .L_dyn_err
    movq    %rax, %rdi
    call    _fc_rt_print_err
.L_dyn_err:
    movl    $1, %eax
    popq    %rbp
    ret
.L_dyn_ok:
    xorl    %eax, %eax
    popq    %rbp
    ret

.section .note.GNU-stack,"",@progbits
