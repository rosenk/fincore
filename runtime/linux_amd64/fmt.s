# runtime/linux_amd64/fmt.s — zero-allocation formatTo writer in x86_64 asm.
#
# fc_fmt struct (88 bytes, allocated on caller's stack):
#   +0:  dst_ptr (8)
#   +8:  dst_len (8)
#   +16: n       (8)
#   +24: scratch (64 bytes, for right-justified Format methods)

.text

# _fc_rt_fmt_init(%rdi = f, %rsi = dst_ptr, %rdx = dst_len)
.global _fc_rt_fmt_init
_fc_rt_fmt_init:
    movq    %rsi, 0(%rdi)
    movq    %rdx, 8(%rdi)
    movq    $0, 16(%rdi)
    ret

# _fc_rt_fmt_n(%rdi = f) -> %rax
.global _fc_rt_fmt_n
_fc_rt_fmt_n:
    movq    16(%rdi), %rax
    ret

# internal: fmt_put(%rdi = f, %sil = byte)
.L_fmt_put:
    movq    16(%rdi), %rax
    cmpq    8(%rdi), %rax
    jae     .L_fp_drop
    movq    0(%rdi), %rcx
    movb    %sil, (%rcx, %rax)
    incq    %rax
    movq    %rax, 16(%rdi)
.L_fp_drop:
    ret

# internal: fmt_fill(%rdi = f, %sil = ch, %rdx = count)
.L_fmt_fill:
    testq   %rdx, %rdx
    jle     .L_ff_done
    movq    16(%rdi), %rax
    movq    8(%rdi), %r8
    movq    0(%rdi), %rcx
.L_ff_loop:
    cmpq    %r8, %rax
    jae     .L_ff_end
    movb    %sil, (%rcx, %rax)
    incq    %rax
    decq    %rdx
    jnz     .L_ff_loop
.L_ff_end:
    movq    %rax, 16(%rdi)
.L_ff_done:
    ret

# internal: fmt_pad_left(%rdi = f, %rsi = item_len, %rdx = width)
.L_fmt_pad_left:
    cmpq    %rsi, %rdx
    jle     .L_fpl_done
    subq    %rsi, %rdx
    movb    $' ', %sil
    jmp     .L_fmt_fill
.L_fpl_done:
    ret

# internal: fmt_pad_right(%rdi = f, %rsi = item_len, %rdx = width)
.L_fmt_pad_right:
    testq   %rdx, %rdx
    jge     .L_fpr_done
    negq    %rdx
    cmpq    %rsi, %rdx
    jle     .L_fpr_done
    subq    %rsi, %rdx
    movb    $' ', %sil
    jmp     .L_fmt_fill
.L_fpr_done:
    ret

# _fc_rt_fmt_lit(%rdi = f, %rsi = ptr, %rdx = len)
.global _fc_rt_fmt_lit
_fc_rt_fmt_lit:
    testq   %rdx, %rdx
    jz      .L_fl_done
    movq    16(%rdi), %rax
    movq    8(%rdi), %r8
    subq    %rax, %r8         # rem room
    jbe     .L_fl_done
    cmpq    %r8, %rdx
    jbe     .L_fl_cp
    movq    %r8, %rdx
.L_fl_cp:
    movq    0(%rdi), %r9
    leaq    (%r9, %rax), %r9
    addq    %rdx, %rax
    movq    %rax, 16(%rdi)
    xorq    %rcx, %rcx
.L_fl_loop:
    movb    (%rsi, %rcx), %al
    movb    %al, (%r9, %rcx)
    incq    %rcx
    cmpq    %rdx, %rcx
    jb      .L_fl_loop
.L_fl_done:
    ret

# _fc_rt_fmt_str(%rdi = f, %rsi = ptr, %rdx = len, %rcx = width)
.global _fc_rt_fmt_str
_fc_rt_fmt_str:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    movq    %rdi, %rbx
    movq    %rsi, %r12
    movq    %rdx, %r13
    movq    %rcx, %r14
    movq    %r13, %rsi
    movq    %r14, %rdx
    call    .L_fmt_pad_left
    movq    %rbx, %rdi
    movq    %r12, %rsi
    movq    %r13, %rdx
    call    _fc_rt_fmt_lit
    movq    %rbx, %rdi
    movq    %r13, %rsi
    movq    %r14, %rdx
    call    .L_fmt_pad_right
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_fmt_byte(%rdi = f, %rsi = ch, %rdx = width)
.global _fc_rt_fmt_byte
_fc_rt_fmt_byte:
    pushq   %rbp
    movq    %rsp, %rbp
    subq    $16, %rsp
    movb    %sil, (%rsp)
    movq    %rdx, %rcx
    movq    %rsp, %rsi
    movq    $1, %rdx
    call    _fc_rt_fmt_str
    movq    %rbp, %rsp
    popq    %rbp
    ret

# _fc_rt_fmt_bool(%rdi = f, %rsi = b, %rdx = width)
.global _fc_rt_fmt_bool
_fc_rt_fmt_bool:
    movq    %rdx, %rcx
    testb   %sil, %sil
    jz      .L_fb_false
    leaq    .L_str_true(%rip), %rsi
    movq    $4, %rdx
    jmp     _fc_rt_fmt_str
.L_fb_false:
    leaq    .L_str_false(%rip), %rsi
    movq    $5, %rdx
    jmp     _fc_rt_fmt_str

# internal: fmt_num(%rdi = f, %rsi = mag(u64), %rdx = neg(0/1), %rcx = base(10/16), %r8 = width, %r9 = zero)
.L_fmt_num:
    pushq   %rbp
    movq    %rsp, %rbp
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    subq    $40, %rsp
    movq    %rdi, %rbx        # f
    movq    %rdx, %r12        # neg
    movq    %r8,  %r13        # width
    movq    %r9,  %r14        # zero
    movq    %rsi, %rax        # mag
    leaq    32(%rsp), %r15    # end of digits
    movq    %r15, %r10
    leaq    .L_hex_digits(%rip), %r11
.L_fn_loop:
    xorq    %rdx, %rdx
    divq    %rcx
    movb    (%r11, %rdx), %dl
    decq    %r10
    movb    %dl, (%r10)
    testq   %rax, %rax
    jnz     .L_fn_loop
    # digits at r10, ndigits = r15 - r10
    subq    %r10, %r15        # r15 = ndigits
    # total item len = ndigits + neg
    movq    %r15, %r8
    addq    %r12, %r8         # r8 = total_len
    pushq   %r10              # save digits ptr
    pushq   %r8               # save total_len
    # If zero && width > total_len: emit '-', fill '0', emit digits
    testq   %r14, %r14
    jz      .L_fn_space
    cmpq    %r8, %r13
    jle     .L_fn_space
    testq   %r12, %r12
    jz      .L_fn_zpad
    movq    %rbx, %rdi
    movb    $'-', %sil
    call    .L_fmt_put
.L_fn_zpad:
    movq    %rbx, %rdi
    movb    $'0', %sil
    movq    (%rsp), %rax      # total_len
    movq    %r13, %rdx
    subq    %rax, %rdx
    call    .L_fmt_fill
    jmp     .L_fn_digits
.L_fn_space:
    movq    %rbx, %rdi
    movq    (%rsp), %rsi      # total_len
    movq    %r13, %rdx
    call    .L_fmt_pad_left
    testq   %r12, %r12
    jz      .L_fn_digits
    movq    %rbx, %rdi
    movb    $'-', %sil
    call    .L_fmt_put
.L_fn_digits:
    movq    %rbx, %rdi
    movq    8(%rsp), %rsi     # digits ptr
    movq    %r15, %rdx        # ndigits
    call    _fc_rt_fmt_lit
    movq    %rbx, %rdi
    movq    (%rsp), %rsi      # total_len
    movq    %r13, %rdx
    call    .L_fmt_pad_right
    addq    $56, %rsp
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    popq    %rbp
    ret

# _fc_rt_fmt_uint(%rdi = f, %rsi = val, %rdx = width, %rcx = zero)
.global _fc_rt_fmt_uint
_fc_rt_fmt_uint:
    movq    %rdx, %r8
    movq    %rcx, %r9
    xorq    %rdx, %rdx        # neg = 0
    movq    $10, %rcx
    jmp     .L_fmt_num

# _fc_rt_fmt_hex(%rdi = f, %rsi = val, %rdx = width, %rcx = zero)
.global _fc_rt_fmt_hex
_fc_rt_fmt_hex:
    movq    %rdx, %r8
    movq    %rcx, %r9
    xorq    %rdx, %rdx
    movq    $16, %rcx
    jmp     .L_fmt_num

# _fc_rt_fmt_int(%rdi = f, %rsi = val, %rdx = width, %rcx = zero)
.global _fc_rt_fmt_int
_fc_rt_fmt_int:
    movq    %rdx, %r8
    movq    %rcx, %r9
    xorq    %rdx, %rdx
    testq   %rsi, %rsi
    jge     .L_fi_pos
    negq    %rsi
    movq    $1, %rdx
.L_fi_pos:
    movq    $10, %rcx
    jmp     .L_fmt_num

# _fc_rt_fmt_hex_int(%rdi = f, %rsi = val, %rdx = width, %rcx = zero)
.global _fc_rt_fmt_hex_int
_fc_rt_fmt_hex_int:
    movq    %rdx, %r8
    movq    %rcx, %r9
    xorq    %rdx, %rdx
    testq   %rsi, %rsi
    jge     .L_fhi_pos
    negq    %rsi
    movq    $1, %rdx
.L_fhi_pos:
    movq    $16, %rcx
    jmp     .L_fmt_num

# _fc_rt_fmt_quote(%rdi = f, %rsi = ptr, %rdx = len, %rcx = width)
.global _fc_rt_fmt_quote
_fc_rt_fmt_quote:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    movq    %rdi, %rbx        # f
    movq    %rsi, %r12        # ptr
    movq    %rdx, %r13        # len
    movq    %rcx, %r14        # width
    # First compute total quoted length in r15
    movq    $2, %r15
    xorq    %rcx, %rcx
.L_fq_len:
    cmpq    %r13, %rcx
    jae     .L_fq_emit
    movzbl  (%r12, %rcx), %eax
    cmpb    $'\n', %al
    je      .L_fq_l2
    cmpb    $'\r', %al
    je      .L_fq_l2
    cmpb    $'\t', %al
    je      .L_fq_l2
    cmpb    $'\\', %al
    je      .L_fq_l2
    cmpb    $'"', %al
    je      .L_fq_l2
    cmpb    $0x20, %al
    jb      .L_fq_l4
    cmpb    $0x7f, %al
    jae     .L_fq_l4
    incq    %r15
    jmp     .L_fq_lnext
.L_fq_l2:
    addq    $2, %r15
    jmp     .L_fq_lnext
.L_fq_l4:
    addq    $4, %r15
.L_fq_lnext:
    incq    %rcx
    jmp     .L_fq_len
.L_fq_emit:
    movq    %rbx, %rdi
    movq    %r15, %rsi
    movq    %r14, %rdx
    call    .L_fmt_pad_left
    movq    %rbx, %rdi
    movb    $'"', %sil
    call    .L_fmt_put
    xorq    %rcx, %rcx
.L_fq_eloop:
    cmpq    %r13, %rcx
    jae     .L_fq_end
    movzbl  (%r12, %rcx), %eax
    pushq   %rcx
    cmpb    $'\n', %al
    je      .L_fq_en
    cmpb    $'\r', %al
    je      .L_fq_er
    cmpb    $'\t', %al
    je      .L_fq_et
    cmpb    $'\\', %al
    je      .L_fq_ebs
    cmpb    $'"', %al
    je      .L_fq_eq
    cmpb    $0x20, %al
    jb      .L_fq_eh
    cmpb    $0x7f, %al
    jae     .L_fq_eh
    movq    %rbx, %rdi
    movb    %al, %sil
    call    .L_fmt_put
    jmp     .L_fq_enext
.L_fq_en:
    movb    $'n', %dl
    jmp     .L_fq_e2
.L_fq_er:
    movb    $'r', %dl
    jmp     .L_fq_e2
.L_fq_et:
    movb    $'t', %dl
    jmp     .L_fq_e2
.L_fq_ebs:
    movb    $'\\', %dl
    jmp     .L_fq_e2
.L_fq_eq:
    movb    $'"', %dl
.L_fq_e2:
    pushq   %rdx
    movq    %rbx, %rdi
    movb    $'\\', %sil
    call    .L_fmt_put
    popq    %rsi
    movq    %rbx, %rdi
    call    .L_fmt_put
    jmp     .L_fq_enext
.L_fq_eh:
    pushq   %rax
    movq    %rbx, %rdi
    movb    $'\\', %sil
    call    .L_fmt_put
    movq    %rbx, %rdi
    movb    $'x', %sil
    call    .L_fmt_put
    movq    (%rsp), %rax
    shrq    $4, %rax
    leaq    .L_hex_digits(%rip), %rdx
    movb    (%rdx, %rax), %sil
    movq    %rbx, %rdi
    call    .L_fmt_put
    popq    %rax
    andq    $15, %rax
    leaq    .L_hex_digits(%rip), %rdx
    movb    (%rdx, %rax), %sil
    movq    %rbx, %rdi
    call    .L_fmt_put
.L_fq_enext:
    popq    %rcx
    incq    %rcx
    jmp     .L_fq_eloop
.L_fq_end:
    movq    %rbx, %rdi
    movb    $'"', %sil
    call    .L_fmt_put
    movq    %rbx, %rdi
    movq    %r15, %rsi
    movq    %r14, %rdx
    call    .L_fmt_pad_right
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_fmt_dst(%rdi = f, %rsi = width) -> (%rax = ptr, %rdx = len, %rcx = in_scratch)
.global _fc_rt_fmt_dst
_fc_rt_fmt_dst:
    movq    8(%rdi), %rdx
    subq    16(%rdi), %rdx    # rem = dst_len - n
    cmpq    $0, %rsi
    jle     .L_fd_direct
    cmpq    %rsi, %rdx
    jae     .L_fd_direct
    # Right-justified and remaining room < width: use scratch[0:width]
    leaq    24(%rdi), %rax
    movq    %rsi, %rdx
    movq    $1, %rcx
    ret
.L_fd_direct:
    movq    0(%rdi), %rax
    addq    16(%rdi), %rax
    xorq    %rcx, %rcx
    ret

# _fc_rt_fmt_advance(%rdi = f, %rsi = written, %rdx = width, %rcx = in_scratch)
.global _fc_rt_fmt_advance
_fc_rt_fmt_advance:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    movq    %rdi, %rbx
    movq    %rsi, %r12        # written
    movq    %rdx, %r13        # width
    testq   %rcx, %rcx
    jz      .L_fa_direct
    # Clamp written to width
    cmpq    %r13, %r12
    jbe     .L_fa_sc_ok
    movq    %r13, %r12
.L_fa_sc_ok:
    movq    %rbx, %rdi
    leaq    24(%rbx), %rsi
    movq    %r12, %rdx
    movq    %r13, %rcx
    call    _fc_rt_fmt_str
    popq    %r13
    popq    %r12
    popq    %rbx
    ret
.L_fa_direct:
    # Clamp written to rem
    movq    8(%rbx), %rax
    subq    16(%rbx), %rax
    cmpq    %rax, %r12
    jbe     .L_fa_cl_ok
    movq    %rax, %r12
.L_fa_cl_ok:
    # If width > written: shift buf[n:n+written] right by (width - written) and fill spaces
    cmpq    %r12, %r13
    jle     .L_fa_adv_only
    movq    %r13, %r8
    subq    %r12, %r8         # pad = width - written
    movq    0(%rbx), %r9
    addq    16(%rbx), %r9     # base = dst + n
    # memmove(base + pad, base, written)
    leaq    (%r9, %r8), %rdi
    movq    %r9, %rsi
    movq    %r12, %rdx
    pushq   %r8
    pushq   %r9
    call    _fc_rt_memmove
    popq    %r9
    popq    %r8
    xorq    %rcx, %rcx
.L_fa_sp:
    movb    $' ', (%r9, %rcx)
    incq    %rcx
    cmpq    %r8, %rcx
    jb      .L_fa_sp
    addq    %r13, 16(%rbx)
    popq    %r13
    popq    %r12
    popq    %rbx
    ret
.L_fa_adv_only:
    addq    %r12, 16(%rbx)
    movq    %rbx, %rdi
    movq    %r12, %rsi
    movq    %r13, %rdx
    call    .L_fmt_pad_right
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

.section .rodata
.L_hex_digits: .ascii "0123456789abcdef"
.L_str_true:   .ascii "true"
.L_str_false:  .ascii "false"
.L_str_nan:    .ascii "NaN"
.L_str_pinf:   .ascii "+Inf"
.L_str_ninf:   .ascii "-Inf"
.balign 8
.L_f64_1e6:    .quad 0x412e848000000000 # 1000000.0
.L_f64_half:   .quad 0x3fe0000000000000 # 0.5
.L_f64_limit:  .quad 0x426d1a94a2000000 # 1e12

.text
# _fc_rt_fmt_f64(%rdi = f, %rsi = ieee754_u64_bits, %rdx = width)
.global _fc_rt_fmt_f64
_fc_rt_fmt_f64:
    pushq   %rbp
    movq    %rsp, %rbp
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    subq    $64, %rsp           # buf[48] at 0(%rsp)
    movq    %rdi, %rbx          # f
    movq    %rdx, %r13          # width
    xorq    %r12, %r12          # neg = 0
    btq     $63, %rsi
    jnc     .L_ff64_pos
    movq    $1, %r12
    btrq    $63, %rsi
.L_ff64_pos:
    # Check NaN / Inf: exp == 0x7ff
    movq    %rsi, %rax
    shrq    $52, %rax
    cmpq    $0x7ff, %rax
    jne     .L_ff64_finite
    movq    $0x000fffffffffffff, %rcx
    testq   %rcx, %rsi
    jnz     .L_ff64_nan
    testq   %r12, %r12
    jnz     .L_ff64_ninf
    movq    %rbx, %rdi
    leaq    .L_str_pinf(%rip), %rsi
    movq    $4, %rdx
    movq    %r13, %rcx
    call    _fc_rt_fmt_str
    jmp     .L_ff64_done
.L_ff64_ninf:
    movq    %rbx, %rdi
    leaq    .L_str_ninf(%rip), %rsi
    movq    $4, %rdx
    movq    %r13, %rcx
    call    _fc_rt_fmt_str
    jmp     .L_ff64_done
.L_ff64_nan:
    movq    %rbx, %rdi
    leaq    .L_str_nan(%rip), %rsi
    movq    $3, %rdx
    movq    %r13, %rcx
    call    _fc_rt_fmt_str
    jmp     .L_ff64_done
.L_ff64_finite:
    movq    %rsi, %xmm0
    ucomisd .L_f64_limit(%rip), %xmm0
    jae     .L_ff64_large
    mulsd   .L_f64_1e6(%rip), %xmm0
    addsd   .L_f64_half(%rip), %xmm0
    cvttsd2siq %xmm0, %rax
    xorq    %rdx, %rdx
    movq    $1000000, %rcx
    divq    %rcx                # rax = intPart, rdx = fracPart (0..999999)
    jmp     .L_ff64_emit
.L_ff64_large:
    cvttsd2siq %xmm0, %rax
    xorq    %rdx, %rdx
.L_ff64_emit:
    # Write backwards into buf[48] starting from 48(%rsp)
    leaq    48(%rsp), %r14
    movq    %r14, %r10
    pushq   %rax                # save intPart
    # Emit 6 fractional digits
    movq    %rdx, %rax
    movq    $6, %r8
    movq    $10, %rcx
.L_ff64_frac:
    xorq    %rdx, %rdx
    divq    %rcx
    addb    $'0', %dl
    decq    %r10
    movb    %dl, (%r10)
    decq    %r8
    jnz     .L_ff64_frac
    decq    %r10
    movb    $'.', (%r10)
    # Emit integer digits
    popq    %rax
.L_ff64_int:
    xorq    %rdx, %rdx
    divq    %rcx
    addb    $'0', %dl
    decq    %r10
    movb    %dl, (%r10)
    testq   %rax, %rax
    jnz     .L_ff64_int
    testq   %r12, %r12
    jz      .L_ff64_call
    decq    %r10
    movb    $'-', (%r10)
.L_ff64_call:
    movq    %r14, %rdx
    subq    %r10, %rdx          # len
    movq    %rbx, %rdi
    movq    %r10, %rsi
    movq    %r13, %rcx
    call    _fc_rt_fmt_str
.L_ff64_done:
    addq    $64, %rsp
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    popq    %rbp
    ret
