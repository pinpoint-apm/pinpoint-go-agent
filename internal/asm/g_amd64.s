#include "go_tls.h"
#include "textflag.h"

TEXT ·Getg(SB), NOSPLIT, $0-8
    get_tls(CX)
    MOVQ    g(CX), AX
    MOVQ    AX, ret+0(FP)
    RET
