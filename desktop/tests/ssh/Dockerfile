FROM caddy:2 AS caddy-binary
FROM ubuntu:22.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends openssh-server python3 && rm -rf /var/lib/apt/lists/*
# Disposable fixture password only; this image is never shipped or installed on a VPS.
RUN echo 'root:dockyard-fixture-only' | chpasswd && useradd --system --create-home --home-dir /var/lib/dockyard-link --shell /usr/sbin/nologin dockyard-link && usermod --password '*' dockyard-link && mkdir -p /run/sshd /var/lib/dockyard-link/.ssh
COPY --from=caddy-binary /usr/bin/caddy /usr/bin/caddy
COPY desktop/tests/ssh/sshd_config /etc/ssh/sshd_config
COPY desktop/tests/ssh/echo.py /fixture/echo.py
COPY desktop/tests/ssh/start.sh /fixture/start.sh
COPY desktop/installer/ /fixture/installer/
CMD ["/bin/sh", "/fixture/start.sh"]
