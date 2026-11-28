# runtime/linux_amd64/net.s — zero-libc Context, Signals, and interruptible TCP Net
# directly over Linux x86_64 syscalls (pipe2, poll, rt_sigaction, socket, bind,
# listen, accept4, connect, read, write, shutdown, close).

.text

.equ SYS_read,        0
.equ SYS_write,       1
.equ SYS_close,       3
.equ SYS_poll,        7
.equ SYS_rt_sigaction,13
.equ SYS_rt_sigreturn,15
.equ SYS_socket,      41
.equ SYS_connect,     42
.equ SYS_shutdown,    48
.equ SYS_bind,        49
.equ SYS_listen,      50
.equ SYS_getsockname, 51
.equ SYS_setsockopt,  54
.equ SYS_getsockopt,  55
.equ SYS_accept4,     288
.equ SYS_pipe2,       293

.equ AF_INET,         2
.equ SOCK_STREAM,     1
.equ SOCK_NONBLOCK,   0x800
.equ SOCK_CLOEXEC,    0x80000
.equ SOL_SOCKET,      1
.equ SO_REUSEADDR,    2
.equ SO_ERROR,        4
.equ SHUT_WR,         1
.equ O_NONBLOCK,      0x800
.equ O_CLOEXEC,       0x80000
.equ SA_RESTORER,     0x04000000
.equ POLLIN,          1
.equ POLLOUT,         4
.equ EINTR,           4
.equ EBADF,           9
.equ EAGAIN,          11
.equ EINPROGRESS,     115

.equ ERR_NIL,         0
.equ ERR_EOF,         1
.equ ERR_TIMEOUT,     2
.equ ERR_CANCELLED,   3
.equ ERR_ZEROWRITE,   4
.equ ERR_CLOSED,      5
.equ ERR_IO,          6

.equ CTX_POOL_CAP,    64

# fc_ctx_state (16 bytes):
#   +0:  cancelled (u32, atomic)
#   +4:  rfd       (i32)
#   +8:  wfd       (i32, atomically xchg'd with -1 on cancel)
#   +12: used      (u32)

# ---------------------------------------------------------------------------
# Context & Signals

# _fc_rt_new_ctx() -> %rax = state_ptr (or 0 if pool exhausted)
.global _fc_rt_new_ctx
_fc_rt_new_ctx:
    pushq   %rbx
    subq    $16, %rsp
    leaq    _fc_ctx_pool(%rip), %rbx
    xorq    %rcx, %rcx
.L_nc_find:
    cmpq    $CTX_POOL_CAP, %rcx
    jae     .L_nc_none
    xorl    %eax, %eax
    movl    $1, %edx
    lock cmpxchgl %edx, 12(%rbx)
    je      .L_nc_got
    addq    $16, %rbx
    incq    %rcx
    jmp     .L_nc_find
.L_nc_got:
    movl    $0, 0(%rbx)
    movl    $-1, 4(%rbx)
    movl    $-1, 8(%rbx)
    movq    $SYS_pipe2, %rax
    movq    %rsp, %rdi
    movq    $(O_NONBLOCK | O_CLOEXEC), %rsi
    syscall
    testq   %rax, %rax
    jl      .L_nc_nopipe
    movl    0(%rsp), %eax
    movl    %eax, 4(%rbx)     # rfd
    movl    4(%rsp), %eax
    movl    %eax, 8(%rbx)     # wfd
.L_nc_nopipe:
    movq    %rbx, %rax
    addq    $16, %rsp
    popq    %rbx
    ret
.L_nc_none:
    xorq    %rax, %rax
    addq    $16, %rsp
    popq    %rbx
    ret

# _fc_rt_ctx_cancel_state(%rdi = state_ptr) — idempotent, async-signal-safe
.weak _fc_rt_fiber_wake_all
.global _fc_rt_ctx_cancel_state
_fc_rt_ctx_cancel_state:
    testq   %rdi, %rdi
    jz      .L_ccs_done
    movl    $1, %eax
    xchgl   %eax, 0(%rdi)     # cancelled = 1 (atomic)
    movl    $-1, %eax
    xchgl   %eax, 8(%rdi)     # atomically take wfd
    cmpl    $0, %eax
    jl      .L_ccs_wake_fibers
    movslq  %eax, %rdi
    movq    $SYS_close, %rax
    syscall
.L_ccs_wake_fibers:
    leaq    _fc_rt_fiber_wake_all(%rip), %rax
    testq   %rax, %rax
    jz      .L_ccs_done
    call    *%rax
.L_ccs_done:
    ret

# _fc_rt_cancel_fn — FinCore ABI closure (CancelFunc: %r12 = state_ptr, no args/rets)
.global _fc_rt_cancel_fn
_fc_rt_cancel_fn:
    pushq   %rbp
    movq    %rsp, %rbp
    movq    %r12, %rdi
    call    _fc_rt_ctx_cancel_state
    popq    %rbp
    ret

# _fc_rt_ctx_err(%rdi = state_ptr, %rsi = deadline_ns) -> %rax = err
.global _fc_rt_ctx_err
_fc_rt_ctx_err:
    testq   %rdi, %rdi
    jz      .L_ce_dl
    movl    0(%rdi), %eax
    testl   %eax, %eax
    jz      .L_ce_dl
    movq    $ERR_CANCELLED, %rax
    ret
.L_ce_dl:
    testq   %rsi, %rsi
    jz      .L_ce_ok
    pushq   %rsi
    call    _fc_rt_clock_now
    popq    %rsi
    cmpq    %rsi, %rax
    jb      .L_ce_ok
    movq    $ERR_TIMEOUT, %rax
    ret
.L_ce_ok:
    xorq    %rax, %rax
    ret

# _fc_rt_ctx_done(%rdi = state_ptr, %rsi = deadline_ns) -> %rax (0/1)
.global _fc_rt_ctx_done
_fc_rt_ctx_done:
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    setne   %al
    movzbq  %al, %rax
    ret

# _fc_rt_ctx_with_timeout(%rdi = old_deadline, %rsi = dur_ns) -> %rax = new_deadline
.global _fc_rt_ctx_with_timeout
_fc_rt_ctx_with_timeout:
    pushq   %rdi
    pushq   %rsi
    call    _fc_rt_clock_now
    popq    %rsi
    popq    %rdi
    addq    %rsi, %rax
    testq   %rax, %rax
    jnz     .L_cwt_nz
    movq    $1, %rax
.L_cwt_nz:
    testq   %rdi, %rdi
    jz      .L_cwt_ret
    cmpq    %rdi, %rax
    jbe     .L_cwt_ret
    movq    %rdi, %rax
.L_cwt_ret:
    ret

# Async-signal-safe restorer and handler
.L_sig_restorer:
    movq    $SYS_rt_sigreturn, %rax
    syscall

.L_sig_handler:
    movq    _fc_sig_ctx(%rip), %rdi
    call    _fc_rt_ctx_cancel_state
    ret

# _fc_rt_sig_notify(%rdi = sig_bitmask) -> %rax = state_ptr
.global _fc_rt_sig_notify
_fc_rt_sig_notify:
    pushq   %rbx
    pushq   %r12
    subq    $32, %rsp         # kernel_sigaction (32 bytes)
    movq    %rdi, %rbx        # sig mask
    call    _fc_rt_new_ctx
    movq    %rax, %r12
    movq    %r12, _fc_sig_ctx(%rip)
    # Prepare sigaction
    leaq    .L_sig_handler(%rip), %rax
    movq    %rax, 0(%rsp)
    movq    $SA_RESTORER, 8(%rsp)
    leaq    .L_sig_restorer(%rip), %rax
    movq    %rax, 16(%rsp)
    movq    $0, 24(%rsp)
    # Install for each bit 1..31 set in rbx
    movq    $1, %rcx
.L_sn_loop:
    cmpq    $32, %rcx
    jae     .L_sn_done
    btq     %rcx, %rbx
    jnc     .L_sn_next
    pushq   %rcx
    movq    $SYS_rt_sigaction, %rax
    movq    %rcx, %rdi
    leaq    8(%rsp), %rsi
    xorq    %rdx, %rdx
    movq    $8, %r10
    syscall
    popq    %rcx
.L_sn_next:
    incq    %rcx
    jmp     .L_sn_loop
.L_sn_done:
    movq    %r12, %rax
    addq    $32, %rsp
    popq    %r12
    popq    %rbx
    ret

# ---------------------------------------------------------------------------
# Internal: poll one fd + optional ctx cancel pipe with optional deadline
# _fc_rt_poll_wait(%rdi = fd, %rsi = events(POLLIN/POLLOUT), %rdx = ctx_state, %rcx = ctx_deadline) -> %rax = err
.weak _fc_rt_fiber_poll_wait
.L_poll_wait:
    movq    %fs:0, %rax
    testq   %rax, %rax
    jz      .L_pw_os_thread
    leaq    _fc_rt_fiber_poll_wait(%rip), %rax
    testq   %rax, %rax
    jz      .L_pw_os_thread
    jmp     *%rax
.L_pw_os_thread:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    subq    $24, %rsp         # struct pollfd pfds[2] (16 bytes)
    movq    %rdi, %rbx        # fd
    movq    %rsi, %r12        # events
    movq    %rdx, %r13        # ctx_state
    movq    %rcx, %r14        # ctx_deadline
.L_pw_retry:
    movq    %r13, %rdi
    movq    %r14, %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_pw_ret
    # Compute timeout_ms in %rdx (-1 if no deadline)
    movq    $-1, %r8
    testq   %r14, %r14
    jz      .L_pw_setup
    call    _fc_rt_clock_now
    movq    %r14, %rcx
    subq    %rax, %rcx
    jle     .L_pw_timeout
    leaq    999999(%rcx), %rax
    xorq    %rdx, %rdx
    movq    $1000000, %r9
    divq    %r9
    movq    %rax, %r8
.L_pw_setup:
    movl    %ebx, 0(%rsp)
    movw    %r12w, 4(%rsp)
    movw    $0, 6(%rsp)
    movq    $1, %rsi          # nfds = 1
    testq   %r13, %r13
    jz      .L_pw_do
    movl    4(%r13), %eax     # cancel rfd
    cmpl    $0, %eax
    jl      .L_pw_do
    movl    %eax, 8(%rsp)
    movw    $POLLIN, 12(%rsp)
    movw    $0, 14(%rsp)
    movq    $2, %rsi
.L_pw_do:
    movq    $SYS_poll, %rax
    movq    %rsp, %rdi
    movq    %r8, %rdx
    syscall
    cmpq    $-EINTR, %rax
    je      .L_pw_retry
    testq   %rax, %rax
    jl      .L_pw_io
    jz      .L_pw_retry       # timeout: loop will report ERR_TIMEOUT
    # Check if ctx cancelled
    movq    %r13, %rdi
    movq    %r14, %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_pw_ret
    # Ready on fd!
    xorq    %rax, %rax
    jmp     .L_pw_ret
.L_pw_timeout:
    movq    $ERR_TIMEOUT, %rax
    jmp     .L_pw_ret
.L_pw_io:
    movq    $ERR_IO, %rax
.L_pw_ret:
    addq    $24, %rsp
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# ---------------------------------------------------------------------------
# IPv4 "host:port" parser
# _fc_rt_parse_ipv4_port(%rdi = ptr, %rsi = len) -> (%rax = ipv4_be32, %rdx = port_be16, %rcx = ok)
.L_parse_ipv4_port:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    movq    %rdi, %rbx
    movq    %rsi, %r12
    # Find last ':'
    movq    %r12, %r13
.L_pip_colon:
    testq   %r13, %r13
    jz      .L_pip_bad
    decq    %r13
    cmpb    $':', (%rbx, %r13)
    jne     .L_pip_colon
    # Parse port from r13+1 .. r12
    leaq    1(%r13), %rcx
    cmpq    %r12, %rcx
    jae     .L_pip_bad
    xorq    %r14, %r14        # port
.L_pip_port:
    cmpq    %r12, %rcx
    jae     .L_pip_port_ok
    movzbl  (%rbx, %rcx), %eax
    subl    $'0', %eax
    cmpl    $9, %eax
    ja      .L_pip_bad
    imulq   $10, %r14, %r14
    addq    %rax, %r14
    cmpq    $65535, %r14
    ja      .L_pip_bad
    incq    %rcx
    jmp     .L_pip_port
.L_pip_port_ok:
    # Convert port to big-endian 16-bit in %r14
    movq    %r14, %rax
    xchgb   %al, %ah
    movzwq  %ax, %r14
    # Parse host: ptr=%rbx, len=%r13
    testq   %r13, %r13
    jz      .L_pip_any        # "" -> 0.0.0.0
    # Check "localhost" (len 9)
    cmpq    $9, %r13
    jne     .L_pip_dotted
    movq    %rbx, %rdi
    movq    $9, %rsi
    leaq    .L_localhost(%rip), %rdx
    movq    $9, %rcx
    call    _fc_rt_bytes_eq
    testq   %rax, %rax
    jz      .L_pip_dotted
    movq    $0x0100007f, %rax # 127.0.0.1 in network byte order
    movq    %r14, %rdx
    movq    $1, %rcx
    jmp     .L_pip_ret
.L_pip_any:
    xorq    %rax, %rax        # 0.0.0.0
    movq    %r14, %rdx
    movq    $1, %rcx
    jmp     .L_pip_ret
.L_pip_dotted:
    # Parse 4 decimal octets separated by '.'
    xorq    %rax, %rax        # packed ip_be32
    xorq    %rcx, %rcx        # pos in host
    xorq    %r8, %r8          # octet index 0..3
.L_pip_oct:
    xorq    %r9, %r9          # octet val
    xorq    %r10, %r10        # digit count
.L_pip_dig:
    cmpq    %r13, %rcx
    jae     .L_pip_oct_end
    movzbl  (%rbx, %rcx), %edx
    cmpb    $'.', %dl
    je      .L_pip_oct_end
    subl    $'0', %edx
    cmpl    $9, %edx
    ja      .L_pip_bad
    imulq   $10, %r9, %r9
    addq    %rdx, %r9
    cmpq    $255, %r9
    ja      .L_pip_bad
    incq    %r10
    incq    %rcx
    jmp     .L_pip_dig
.L_pip_oct_end:
    testq   %r10, %r10
    jz      .L_pip_bad
    # Store octet at byte r8 of %rax (network byte order = byte 0 is first octet)
    pushq   %rcx
    movq    %r8, %rcx
    shlq    $3, %rcx
    shlq    %cl, %r9
    orq     %r9, %rax
    popq    %rcx
    incq    %r8
    cmpq    $4, %r8
    je      .L_pip_oct_done
    cmpq    %r13, %rcx
    jae     .L_pip_bad
    cmpb    $'.', (%rbx, %rcx)
    jne     .L_pip_bad
    incq    %rcx
    jmp     .L_pip_oct
.L_pip_oct_done:
    cmpq    %r13, %rcx
    jne     .L_pip_bad
    movq    %r14, %rdx
    movq    $1, %rcx
    jmp     .L_pip_ret
.L_pip_bad:
    xorq    %rax, %rax
    xorq    %rdx, %rdx
    xorq    %rcx, %rcx
.L_pip_ret:
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# ---------------------------------------------------------------------------
# TCP Listener & Conn

# _fc_rt_net_listen_tcp(%rdi = addr_ptr, %rsi = addr_len) -> (%rax = fd, %rdx = packed_ip_port, %rcx = err)
# packed_ip_port: low 32 bits = ip_be32, bits 32..47 = port_be16
.global _fc_rt_net_listen_tcp
_fc_rt_net_listen_tcp:
    pushq   %rbx
    pushq   %r12
    subq    $32, %rsp         # sockaddr_in[16] + optval[4] + addrlen[4]
    call    .L_parse_ipv4_port
    testq   %rcx, %rcx
    jz      .L_lt_err
    movw    $AF_INET, 0(%rsp)
    movw    %dx, 2(%rsp)
    movl    %eax, 4(%rsp)
    movq    $0, 8(%rsp)
    # socket(AF_INET, SOCK_STREAM|SOCK_NONBLOCK|SOCK_CLOEXEC, 0)
    movq    $SYS_socket, %rax
    movq    $AF_INET, %rdi
    movq    $(SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC), %rsi
    xorq    %rdx, %rdx
    syscall
    testq   %rax, %rax
    jl      .L_lt_err
    movq    %rax, %rbx        # fd
    # setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &1, 4)
    movl    $1, 16(%rsp)
    movq    $SYS_setsockopt, %rax
    movq    %rbx, %rdi
    movq    $SOL_SOCKET, %rsi
    movq    $SO_REUSEADDR, %rdx
    leaq    16(%rsp), %r10
    movq    $4, %r8
    syscall
    # bind(fd, &sa, 16)
    movq    $SYS_bind, %rax
    movq    %rbx, %rdi
    movq    %rsp, %rsi
    movq    $16, %rdx
    syscall
    testq   %rax, %rax
    jl      .L_lt_close_err
    # listen(fd, 128)
    movq    $SYS_listen, %rax
    movq    %rbx, %rdi
    movq    $128, %rsi
    syscall
    testq   %rax, %rax
    jl      .L_lt_close_err
    # getsockname(fd, &sa, &len) so port 0 gets real assigned port
    movl    $16, 20(%rsp)
    movq    $SYS_getsockname, %rax
    movq    %rbx, %rdi
    movq    %rsp, %rsi
    leaq    20(%rsp), %rdx
    syscall
    movl    4(%rsp), %edx     # ip_be32 (zero-extended)
    movzwq  2(%rsp), %rax     # port_be16
    shlq    $32, %rax
    orq     %rax, %rdx
    movq    %rbx, %rax        # fd
    xorq    %rcx, %rcx
    addq    $32, %rsp
    popq    %r12
    popq    %rbx
    ret
.L_lt_close_err:
    movq    $SYS_close, %rax
    movq    %rbx, %rdi
    syscall
.L_lt_err:
    movq    $-1, %rax
    xorq    %rdx, %rdx
    movq    $ERR_IO, %rcx
    addq    $32, %rsp
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_listener_addr(%rdi = packed_ip_port) -> (%rax = ptr, %rdx = len)
.global _fc_rt_listener_addr
_fc_rt_listener_addr:
    pushq   %rbx
    pushq   %r12
    subq    $48, %rsp
    movq    %rdi, %r12
    movq    %rsp, %rbx
    # Format 4 octets
    xorq    %rcx, %rcx
.L_la_oct:
    pushq   %rcx
    movq    %r12, %rax
    shlq    $3, %rcx
    shrq    %cl, %rax
    andq    $0xff, %rax
    movq    %rax, %rdi
    movq    %rbx, %rsi
    movq    $8, %rdx
    call    _fc_rt_format_uint
    addq    %rax, %rbx
    popq    %rcx
    incq    %rcx
    cmpq    $4, %rcx
    jae     .L_la_port
    movb    $'.', (%rbx)
    incq    %rbx
    jmp     .L_la_oct
.L_la_port:
    movb    $':', (%rbx)
    incq    %rbx
    movq    %r12, %rax
    shrq    $32, %rax
    xchgb   %al, %ah
    movzwq  %ax, %rdi
    movq    %rbx, %rsi
    movq    $8, %rdx
    call    _fc_rt_format_uint
    addq    %rax, %rbx
    movq    %rbx, %rsi
    subq    %rsp, %rsi
    movq    %rsp, %rdi
    call    _fc_rt_str_bytes
    addq    $48, %rsp
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_listener_accept(%rdi = fd, %rsi = ctx_state, %rdx = ctx_deadline) -> (%rax = conn_fd, %rdx = err)
.global _fc_rt_listener_accept
_fc_rt_listener_accept:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    movq    %rdi, %rbx
    movq    %rsi, %r12
    movq    %rdx, %r13
.L_ac_loop:
    movq    %r12, %rdi
    movq    %r13, %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_ac_err
    movq    $SYS_accept4, %rax
    movq    %rbx, %rdi
    xorq    %rsi, %rsi
    xorq    %rdx, %rdx
    movq    $(SOCK_NONBLOCK | SOCK_CLOEXEC), %r10
    syscall
    testq   %rax, %rax
    jge     .L_ac_ok
    cmpq    $-EINTR, %rax
    je      .L_ac_loop
    cmpq    $-EAGAIN, %rax
    je      .L_ac_wait
    cmpq    $-EBADF, %rax
    je      .L_ac_closed
    movq    $ERR_IO, %rax
    jmp     .L_ac_err
.L_ac_wait:
    movq    %rbx, %rdi
    movq    $POLLIN, %rsi
    movq    %r12, %rdx
    movq    %r13, %rcx
    call    .L_poll_wait
    testq   %rax, %rax
    jz      .L_ac_loop
    jmp     .L_ac_err
.L_ac_closed:
    movq    $ERR_CLOSED, %rax
.L_ac_err:
    movq    %rax, %rdx
    movq    $-1, %rax
    popq    %r13
    popq    %r12
    popq    %rbx
    ret
.L_ac_ok:
    xorq    %rdx, %rdx
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_net_dial_tcp(%rdi = ctx_state, %rsi = ctx_deadline, %rdx = addr_ptr, %rcx = addr_len)
# -> (%rax = conn_fd, %rdx = err)
.global _fc_rt_net_dial_tcp
_fc_rt_net_dial_tcp:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    subq    $32, %rsp
    movq    %rdi, %r12
    movq    %rsi, %r13
    movq    %rdx, %rdi
    movq    %rcx, %rsi
    call    .L_parse_ipv4_port
    testq   %rcx, %rcx
    jz      .L_dt_err_io
    movw    $AF_INET, 0(%rsp)
    movw    %dx, 2(%rsp)
    movl    %eax, 4(%rsp)
    movq    $0, 8(%rsp)
    movq    $SYS_socket, %rax
    movq    $AF_INET, %rdi
    movq    $(SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC), %rsi
    xorq    %rdx, %rdx
    syscall
    testq   %rax, %rax
    jl      .L_dt_err_io
    movq    %rax, %rbx
    movq    $SYS_connect, %rax
    movq    %rbx, %rdi
    movq    %rsp, %rsi
    movq    $16, %rdx
    syscall
    testq   %rax, %rax
    jge     .L_dt_ok
    cmpq    $-EINPROGRESS, %rax
    jne     .L_dt_close_io
    movq    %rbx, %rdi
    movq    $POLLOUT, %rsi
    movq    %r12, %rdx
    movq    %r13, %rcx
    call    .L_poll_wait
    testq   %rax, %rax
    jnz     .L_dt_close_err
    movl    $0, 16(%rsp)
    movl    $4, 20(%rsp)
    movq    $SYS_getsockopt, %rax
    movq    %rbx, %rdi
    movq    $SOL_SOCKET, %rsi
    movq    $SO_ERROR, %rdx
    leaq    16(%rsp), %r10
    leaq    20(%rsp), %r8
    syscall
    cmpl    $0, 16(%rsp)
    jne     .L_dt_close_io
.L_dt_ok:
    movq    %rbx, %rax
    xorq    %rdx, %rdx
    jmp     .L_dt_ret
.L_dt_close_io:
    movq    $ERR_IO, %rax
.L_dt_close_err:
    pushq   %rax
    movq    $SYS_close, %rax
    movq    %rbx, %rdi
    syscall
    popq    %rdx
    movq    $-1, %rax
    jmp     .L_dt_ret
.L_dt_err_io:
    movq    $-1, %rax
    movq    $ERR_IO, %rdx
.L_dt_ret:
    addq    $32, %rsp
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_conn_read(%rdi = fd, %rsi = ctx_state, %rdx = ctx_deadline, %rcx = buf_ptr, %r8 = buf_len)
# -> (%rax = n, %rdx = err)
.global _fc_rt_conn_read
_fc_rt_conn_read:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    movq    %rdi, %rbx
    movq    %rsi, %r12
    movq    %rdx, %r13
    movq    %rcx, %r14
    movq    %r8,  %r15
.L_cr_loop:
    movq    %r12, %rdi
    movq    %r13, %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_cr_err
    movq    $SYS_read, %rax
    movq    %rbx, %rdi
    movq    %r14, %rsi
    movq    %r15, %rdx
    syscall
    cmpq    $-EINTR, %rax
    je      .L_cr_loop
    cmpq    $-EAGAIN, %rax
    je      .L_cr_wait
    testq   %rax, %rax
    jl      .L_cr_io
    jz      .L_cr_eof
    xorq    %rdx, %rdx
    jmp     .L_cr_ret
.L_cr_wait:
    movq    %rbx, %rdi
    movq    $POLLIN, %rsi
    movq    %r12, %rdx
    movq    %r13, %rcx
    call    .L_poll_wait
    testq   %rax, %rax
    jz      .L_cr_loop
    jmp     .L_cr_err
.L_cr_eof:
    xorq    %rax, %rax
    movq    $ERR_EOF, %rdx
    jmp     .L_cr_ret
.L_cr_io:
    movq    $ERR_IO, %rax
.L_cr_err:
    movq    %rax, %rdx
    xorq    %rax, %rax
.L_cr_ret:
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_conn_write(%rdi = fd, %rsi = ctx_state, %rdx = ctx_deadline, %rcx = buf_ptr, %r8 = buf_len)
# -> (%rax = n, %rdx = err)
.global _fc_rt_conn_write
_fc_rt_conn_write:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    movq    %rdi, %rbx
    movq    %rsi, %r12
    movq    %rdx, %r13
    movq    %rcx, %r14
    movq    %r8,  %r15
.L_cw_loop:
    movq    %r12, %rdi
    movq    %r13, %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_cw_err
    movq    $SYS_write, %rax
    movq    %rbx, %rdi
    movq    %r14, %rsi
    movq    %r15, %rdx
    syscall
    cmpq    $-EINTR, %rax
    je      .L_cw_loop
    cmpq    $-EAGAIN, %rax
    je      .L_cw_wait
    testq   %rax, %rax
    jl      .L_cw_io
    xorq    %rdx, %rdx
    jmp     .L_cw_ret
.L_cw_wait:
    movq    %rbx, %rdi
    movq    $POLLOUT, %rsi
    movq    %r12, %rdx
    movq    %r13, %rcx
    call    .L_poll_wait
    testq   %rax, %rax
    jz      .L_cw_loop
    jmp     .L_cw_err
.L_cw_io:
    movq    $ERR_IO, %rax
.L_cw_err:
    movq    %rax, %rdx
    xorq    %rax, %rax
.L_cw_ret:
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_conn_close_write(%rdi = fd) -> %rax = err
.global _fc_rt_conn_close_write
_fc_rt_conn_close_write:
    movq    $SYS_shutdown, %rax
    movq    $SHUT_WR, %rsi
    syscall
    testq   %rax, %rax
    jge     .L_ccw_ok
    movq    $ERR_IO, %rax
    ret
.L_ccw_ok:
    xorq    %rax, %rax
    ret

# _fc_rt_close_fd(%rdi = fd) -> %rax = err
.global _fc_rt_close_fd
_fc_rt_close_fd:
    testq   %rdi, %rdi
    jl      .L_cfd_ok
    movq    $SYS_close, %rax
    syscall
    testq   %rax, %rax
    jge     .L_cfd_ok
    movq    $ERR_CLOSED, %rax
    ret
.L_cfd_ok:
    xorq    %rax, %rax
    ret

.section .rodata
.L_localhost: .ascii "localhost"

.section .bss
.balign 8
_fc_sig_ctx:   .skip 8
.balign 16
_fc_ctx_pool:  .skip (CTX_POOL_CAP * 16)
