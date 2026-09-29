#!/bin/sh
set -eu
rm -f /etc/ssh/ssh_host_*key*
ssh-keygen -A >/dev/null
python3 /fixture/echo.py &
exec /usr/sbin/sshd -D -e
