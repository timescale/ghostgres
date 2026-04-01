# SSL/TLS Certificate Management

## Current setup

We use a [Let's Encrypt](https://letsencrypt.org/) certificate for `try.ghostgres.com`, issued via `certbot` with a DNS challenge. The cert and key are stored as Fly.io secrets, which get mounted as files at `/etc/tls/tls.crt` and `/etc/tls/tls.key` (configured in `fly.toml`).

Let's Encrypt certificates are valid for **90 days**.

## Issuing or renewing the certificate

1. Run certbot with the DNS challenge:

   ```bash
   sudo certbot certonly --manual --preferred-challenges dns -d try.ghostgres.com
   ```

2. When prompted, create a `_acme-challenge.try.ghostgres.com` TXT DNS record with the provided value. Wait for propagation, then continue.

3. After certbot succeeds, upload the new cert and key to Fly:

   ```bash
   fly secrets set \
     TLS_CERT="$(sudo cat /etc/letsencrypt/live/try.ghostgres.com/fullchain.pem | base64)" \
     TLS_KEY="$(sudo cat /etc/letsencrypt/live/try.ghostgres.com/privkey.pem | base64)"
   ```

4. Deploy (or the secrets update may trigger a restart automatically):

   ```bash
   fly deploy
   ```

5. Remove the `_acme-challenge` TXT record from DNS — it's only needed during issuance.
