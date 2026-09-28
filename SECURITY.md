# Security Policy

## Reporting a Vulnerability

If you believe you have found a security vulnerability in the Ferentin
Terraform provider, please report it to us through coordinated disclosure.

**Do not report security vulnerabilities through public GitHub issues,
discussions, or pull requests.**

Email: security@ferentin.com

Please include as much of the following as you can:

- The type of issue (e.g. a secret written to state or logs, a credential sent
  to the wrong host, TLS verification skipped where it should not be)
- The resource, data source or provider argument affected
- A minimal configuration that reproduces it, with every secret removed
- Terraform version (`terraform version`) and provider version
- Proof-of-concept (if available)
- Impact assessment: what an attacker could achieve

We will acknowledge receipt within **2 business days** and aim to provide a
resolution timeline within **7 business days**.

Vulnerabilities in the Ferentin platform itself, rather than in this provider,
go to the same address.

## Supported Versions

| Version | Supported |
|---|---|
| Latest release | ✅ |
| All prior versions | ❌ |

## Disclosure Policy

- We will confirm the vulnerability and determine its impact.
- We aim to release a fix within **30 days** for critical issues, **90 days**
  for others.
- We will coordinate public disclosure with the reporter.
- We credit researchers in our release notes unless they prefer anonymity.

## Scope

**In scope:**
- The provider binary and every resource and data source it registers
- Provider authentication: shared `ferentin login` profiles, OAuth2
  client_credentials, and static tokens
- Handling of secrets in plan, state and logs
- TLS and redirect behaviour of the provider's connections

**Out of scope:**
- Denial-of-service attacks
- Social engineering of Ferentin staff
- Secrets a configuration places in an ordinary (not write-only) argument, or
  in Terraform state or plan files, which are protected by your state backend
- Issues in Terraform itself or in the OS keyring (report to their vendors)
- Theoretical vulnerabilities without proof of concept

## Verifying a Release

Every release publishes a `SHA256SUMS` file and a detached GPG signature over
it (`SHA256SUMS.sig`). The Terraform registry verifies that signature on
`terraform init`. To verify a download by hand:

```sh
gpg --verify terraform-provider-ferentin_<version>_SHA256SUMS.sig \
             terraform-provider-ferentin_<version>_SHA256SUMS
shasum -a 256 -c terraform-provider-ferentin_<version>_SHA256SUMS --ignore-missing
```

Signing key fingerprint: **published with the first signed release.** Until
then, no release of this provider exists, and anything claiming to be one is
not ours.

## Security Practices

- **Secrets are write-only.** `ferentin_llm_provider`'s `api_key`,
  `credentials` and `external_id`, and `ferentin_workload_oauth_client`'s
  `client_secret` and `private_key_jwt_private_key` are write-only arguments:
  they are sent to the platform and never written to plan or state. Rotate one
  by bumping its `*_wo_version` companion. This needs Terraform 1.11 or later.
- The provider's own `token` and `client_secret` are marked sensitive.
- **TLS is required.** `endpoint` and `auth_url` must be `https://`; `http://`
  is accepted only on `127.0.0.1`, `[::1]` or `localhost`. Certificate
  verification can be relaxed only for a named non-production destination, is
  never relaxed for a `*.ferentin.net` host, and is refused where a managed
  (MDM) configuration forbids it.
- Redirects that would carry a credential are followed only within one origin
  (or from port 80 to 443 on the same host).
- Shared-profile credentials are read from the OS keyring by the Ferentin CLI's
  SDK, under the same cross-process lock the CLI takes, so a concurrent
  `terraform apply` and `ferentin` command cannot corrupt a rotating refresh
  token.
- Error bodies from the platform are bounded and sanitised before they reach a
  diagnostic.
- GitHub Actions pinned to commit SHAs; `govulncheck` scans this module's own
  build on every push and pull request.
