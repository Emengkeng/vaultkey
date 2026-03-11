# Security Policy

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

VaultKey handles sensitive cryptographic operations and private keys. We take security seriously and appreciate responsible disclosure.

### How to Report

Send vulnerability reports to: **[hello@juslen.site](mailto:hello@juslen.site)**

Include in your report:

1. **Description**: What is the vulnerability?
2. **Impact**: What can an attacker do? (e.g., steal keys, bypass auth, DOS)
3. **Affected versions**: Which versions are vulnerable?
4. **Steps to reproduce**: Detailed reproduction steps
5. **Proof of concept**: Code or curl commands demonstrating the issue
6. **Suggested fix**: If you have one (optional)
7. **Disclosure timeline**: When you plan to publicly disclose (we appreciate 90 days)

### What to Expect

- **Acknowledgment**: Within 48 hours
- **Initial assessment**: Within 1 week
- **Regular updates**: Every 7-14 days until resolved
- **Fix timeline**: Critical issues within 7 days, high severity within 30 days
- **Public disclosure**: Coordinated with you after a fix is released
- **Credit**: You'll be credited in the security advisory (unless you prefer anonymity)

### Severity Levels

**Critical** (immediate action):
- Private key exposure
- Authentication bypass
- Remote code execution
- Data exfiltration from Vault

**High** (7-day fix):
- Privilege escalation
- SQL injection
- Webhook signature bypass
- Timing attack on secrets

**Medium** (30-day fix):
- Rate limit bypass
- Information disclosure
- Denial of service

**Low** (best effort):
- Minor information leaks
- Configuration issues

### Supported Versions

We provide security updates for:

| Version | Supported          |
| ------- | ------------------ |
| 0.x.x   | :white_check_mark: |

Once we release 1.0.0, we'll support the latest major version and one prior major version.

### Security Best Practices

When deploying VaultKey:

1. **Network isolation**: Run Vault, Redis, and Postgres on a private network
2. **Secrets management**: Never commit API secrets or Vault tokens
3. **Least privilege**: Use minimal IAM/RBAC permissions
4. **Encryption in transit**: TLS for all external connections
5. **Monitoring**: Enable audit logs and alert on anomalies
6. **Updates**: Subscribe to security advisories and update promptly
7. **Backup Vault keys**: Store unseal keys in a secure, offline location
8. **Key rotation**: Rotate API keys and Vault tokens regularly
9. **Rate limiting**: Configure aggressive rate limits for public endpoints
10. **Webhook verification**: Always verify HMAC signatures on webhook deliveries

### Security Features

VaultKey includes:

- **Keys never leave the service**: Decrypted in-memory only during signing
- **Defense in depth**: Multiple encryption layers (Vault + AES-256-GCM)
- **Audit logging**: Every wallet operation logged to Postgres
- **Constant-time comparisons**: Prevents timing attacks on secrets
- **Idempotency**: Prevents replay attacks via idempotency keys
- **Webhook auth**: HMAC-SHA256 signatures prevent spoofing
- **No key storage**: Keys wiped from memory immediately after use

### Known Limitations

Current security considerations:

1. **Key backup**: No built-in key recovery mechanism (by design). If you lose Vault unseal keys, encrypted wallets are irrecoverable.
2. **Memory safety**: We zero byte slices, but Go's GC may leave key material in memory briefly.
3. **Side-channel attacks**: Not hardened against timing/power analysis (use HSMs for that).
4. **Rate limiting**: Basic sliding window in Redis (consider a WAF for advanced DDoS protection).

### Security Audits

- ❌ No formal security audit yet (planned for Q2 2026)
- ✅ Code reviewed by cryptography engineers
- ✅ Penetration testing: In progress

We welcome security researchers and offer recognition (and potentially bounties in the future).

### Hall of Fame

Security researchers who responsibly disclosed vulnerabilities:

*(None yet - be the first!)*

---

**Questions?** Email [hello@juslen.site](mailto:hello@juslen.site)

Thank you for helping keep VaultKey secure!