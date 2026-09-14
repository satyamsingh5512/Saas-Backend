# Deployment Context — Tenant SaaS Backend → Oracle Cloud

## 1. Project
- Multi-tenant SaaS API + dashboard: Go 1.26 / Gin / GORM / PostgreSQL 16 (RLS) / Redis 7 / nginx TLS.
- Repo: `/home/satym-in/Documents/projects/Saas-Backend`
- Key files:
  - `docs/DEPLOY_ORACLE.md` — canonical 45-min guide (A1 Flex 2/12, Ubuntu 24.04)
  - `docker-compose.production.yml` — app + postgres + redis + nginx (only nginx exposes 80/443)
  - `Dockerfile` — distroless nonroot image
  - `scripts/deploy_oracle.sh` — one-command deploy (validates secrets → renders nginx → starts pg/redis → Let's Encrypt → builds app with embedded migrations → validates endpoints)
  - `deploy/.env.production.example` → copy to `.env.production` on VM
  - `oracle-setup.sh` (this repo root) — OCI infra creator for Cloud Shell (VCN+IGW+route+seclist+subnet+instance). Generates FRESH key at `/tmp/saas-key`, no old key embedded.

## 2. Target infra (intended)
- Tenancy: `satyamsinghpx (root)`
- Region: `ap-hyderabad-1` ONLY (subscribed to Hyderabad only, no region switch)
- AD: `uWIc:AP-HYDERABAD-1-AD-1`
- Shape: `VM.Standard.A1.Flex` **2 OCPU / 12 GB** (Always Free)
- Image: `Canonical Ubuntu 24.04 Minimal aarch64` (ARM; x86 images incompatible with A1)
  - Current IMAGE_ID: `ocid1.image.oc1.ap-hyderabad-1.aaaaaaaamsa6gfbgj3jzzure6bepyaatayhqg23wiighc4pqa66eh43mau3q`
- Network (created, reused on re-run):
  - VCN `saas-vcn`: `ocid1.vcn.oc1.ap-hyderabad-1.amaaaaaaz3ajqxiaisyibii7ep775utuu57rhf6hvsfd6ta44va5vdospbtq` (`10.0.0.0/16`)
  - Subnet `saas-public`: `ocid1.subnet.oc1.ap-hyderabad-1.aaaaaaaacuk3vz5gt5wzr5m7svi6t762caixpbuhh5bcqaazp3gfwuflpwfq` (`10.0.0.0/24`, public, route→IGW, seclist 22/80/443)
  - Requires public IPv4 `Yes` (console new-VCN flow toggle bug → use CLI `--assign-public-ip true`)
- Storage: boot `100 GB / 10 VPU`, in-transit ON, Oracle-managed key (stays under 200 GB free limit)
- Security: Shielded ON (Secure/Measured/TPM greyed on ARM minimal — normal); Confidential OFF (AMD SEV only, A1 has none — expected)
- SSH: fresh `/tmp/saas-key` generated per run (old `ssh-rsa ...WG...3nwf` key explicitly NOT used)

## 3. State as of 2026-09-14 Cloud Shell runs
- VCN + SUBNET created OK.
- Instance launch FAILS:
  ```
  code: InternalError / message: Out of host capacity.
  operation: launch_instance, status 500, AD-1
  ```
- `oracle-setup.sh` now tries `2/12 → 1/6` fallback across all ADs (`jq -r '.data[].name'`; old `--output text` unsupported on CLI 3.90.1).
- Console flow also prepared (name `Tenant-Saas-backend`, image/shape/network/SSH/storage) but blocked on public-IP toggle; CLI path preferred.

## 4. Decisions / constraints from user
- No old SSH key reuse.
- Hyderabad-only subscription (Mumbai/Ashburn switch rejected).
- Automation via Playwright attempted (logged into console, fixed image→aarch64, shape→A1 2/12, VCN/subnet, pasted key, storage 100 GB) but stopped before Create due to public-IP `No`.
- User asked for single-shell creator → `oracle-setup.sh`; then asked to save it as file (done).

## 5. Next steps
1. Retry A1: `while true; do ./oracle-setup.sh && break; sleep 300; done` (capacity frees early-morning UTC).
2. Immediate unblock (trial credits): launch `VM.Standard.E4.Flex 2/12` with x86 `Canonical Ubuntu 24.04` image in same subnet, then migrate to A1 later.
3. On `RUNNING`: capture public IP (`oci compute instance list-vnics`), download `/tmp/saas-key`, `ssh -i saas-key ubuntu@<IP>`, set DNS A-record, run `docs/DEPLOY_ORACLE.md §2-3` (`docker install → git clone → .env.production → ./scripts/deploy_oracle.sh`), then cron backups/janitor + validation table.
