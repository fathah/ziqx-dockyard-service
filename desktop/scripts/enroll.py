#!/usr/bin/env python3
"""Package an operator-provisioned client identity without printing credentials."""
import argparse
import base64
import hashlib
import json
import os
import pathlib
import ssl
import stat


def read(path, private=False):
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size > 65536:
            raise ValueError('Credentials must be regular files up to 64 KiB')
        if private and (info.st_uid != os.getuid() or info.st_mode & 0o077):
            raise ValueError('Private keys and HMAC files must be owned by you with mode 600')
        return stream.read(65537).decode('utf-8')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for field in ('name', 'origin', 'server-id', 'key-id', 'actor-id', 'server-ca', 'server-cert', 'client-cert', 'client-key', 'hmac-file', 'out'):
        parser.add_argument('--' + field, required=True)
    args = parser.parse_args()
    cert = read(args.client_cert)
    ca = read(args.server_ca)
    server_cert = read(args.server_cert)
    server_leaf = server_cert[:server_cert.index('-----END CERTIFICATE-----')+len('-----END CERTIFICATE-----')]
    server_pin = hashlib.sha256(ssl.PEM_cert_to_DER_cert(server_leaf)).hexdigest()
    key = read(args.client_key, True)
    secret = read(args.hmac_file, True).strip()
    decoded = base64.b64decode(secret, validate=True)
    if not 32 <= len(decoded) <= 128:
        raise ValueError('HMAC material must contain 32–128 random bytes')
    # Check PEM syntax and cert/key agreement before exporting.
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.load_verify_locations(cadata=ca)
    context.load_cert_chain(args.client_cert, args.client_key)
    bundle = dict(name=args.name, origin=args.origin, server_id=args.server_id, key_id=args.key_id,
                  actor_id=args.actor_id, server_ca_pem=ca, server_certificate_sha256=server_pin, client_identity_pem=cert.rstrip()+'\n'+key,
                  hmac_base64=secret)
    output = pathlib.Path(args.out).absolute()
    parent = output.parent.stat()
    if parent.st_uid != os.getuid() or parent.st_mode & 0o022:
        raise ValueError('Output directory must be owned by you and not writable by other users')
    raw = (json.dumps(bundle, indent=2)+'\n').encode()
    if len(raw) > 128 * 1024:
        raise ValueError('Enrollment exceeds 128 KiB')
    with os.fdopen(os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), 'wb') as stream:
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())
    first = cert[:cert.index('-----END CERTIFICATE-----')+len('-----END CERTIFICATE-----')]
    fingerprint = hashlib.sha256(ssl.PEM_cert_to_DER_cert(first)).hexdigest()
    print('Enrollment created with mode 600. Import it in Dockyard, then remove the transfer copy.')
    print('Server certificate_sha256 (public client fingerprint): '+fingerprint)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, ssl.SSLError):
        raise SystemExit('Enrollment packaging failed. Check file permissions, PEM credentials, and output directory. No credentials were printed.')
