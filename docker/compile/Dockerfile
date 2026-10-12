FROM gcc:13

ARG YEXJUDGE_CPP_FLAGS
COPY internal/judge/languages/cpp_compile_flags.txt /usr/local/share/yexjudge/cpp_compile_flags.txt

RUN set -eu; \
    flags="$(cat /usr/local/share/yexjudge/cpp_compile_flags.txt)"; \
    if [ -n "${YEXJUDGE_CPP_FLAGS:-}" ]; then \
        test "$YEXJUDGE_CPP_FLAGS" = "$flags"; \
    fi; \
    header="$(find /usr/local/include/c++ -path '*/bits/stdc++.h' -print -quit)"; \
    test -n "$header"; \
    set -f; \
    set -- $flags; \
    g++ "$@" -x c++-header "$header" -o "$header.gch"; \
    printf '#include <bits/stdc++.h>\nint main(){return 0;}\n' > /tmp/yexjudge-pch-check.cpp; \
    g++ "$@" -H -fsyntax-only /tmp/yexjudge-pch-check.cpp 2>/tmp/yexjudge-pch-check.log; \
    grep -F "! $header.gch" /tmp/yexjudge-pch-check.log
