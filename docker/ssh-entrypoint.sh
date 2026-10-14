#!/bin/sh
set -eu

if [ -z "${SSH_PASSWORD:-}" ]; then
    echo "error: SSH_PASSWORD is required" >&2
    exit 1
fi

case "${SSH_PASSWORD}" in
    *:*)
        echo "error: SSH_PASSWORD must not contain ':'" >&2
        exit 1
        ;;
esac

printf 'knowledge:%s\n' "${SSH_PASSWORD}" | chpasswd

if [ ! -f /etc/ssh/host_keys/ssh_host_ed25519_key ]; then
    ssh-keygen -q -t ed25519 -N "" -f /etc/ssh/host_keys/ssh_host_ed25519_key
fi

if [ ! -f /etc/ssh/host_keys/ssh_host_rsa_key ]; then
    ssh-keygen -q -t rsa -b 3072 -N "" -f /etc/ssh/host_keys/ssh_host_rsa_key
fi

exec /usr/sbin/sshd -D -e -f /etc/ssh/sshd_config
