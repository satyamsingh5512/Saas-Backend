#!/usr/bin/env bash
# Oracle A1 Flex setup for Tenant SaaS backend (Always Free: 2 OCPU / 12GB)
# Run once in OCI Cloud Shell (region ap-hyderabad-1).
# Creates: VCN + IGW + route + seclist(22/80/443) + public subnet + A1.Flex 2/12 Ubuntu 24.04 aarch64 100GB + public IP
# Generates a FRESH SSH key at /tmp/saas-key (no old key reused). Download it after run.
set -euo pipefail

export COMPARTMENT_ID=${COMPARTMENT_ID:-$(oci iam tenancy get --tenancy-id "$OCI_TENANCY" --query 'data.id' --raw-output 2>/dev/null || echo "")}
[ -z "${COMPARTMENT_ID:-}" ] && { echo "Set COMPARTMENT_ID to tenancy OCID first: export COMPARTMENT_ID=ocid1.tenancy..."; exit 1; }
NAME=Tenant-Saas-backend; VCN_NAME=saas-vcn; SUBNET_NAME=saas-public

# ---------- fresh SSH key (no old key used) ----------
rm -f /tmp/saas-key /tmp/saas-key.pub
ssh-keygen -t rsa -b 4096 -f /tmp/saas-key -N "" -C "saas-2026-09-14" -q
chmod 600 /tmp/saas-key; echo "Key fingerprint:"; ssh-keygen -lf /tmp/saas-key.pub

# ---------- image (24.04 aarch64 -> fallback 22.04 aarch64) ----------
IMAGE_ID=$(oci compute image list -c $COMPARTMENT_ID --operating-system "Canonical Ubuntu" --operating-system-version "24.04 Minimal aarch64" --shape VM.Standard.A1.Flex --sort-by TIMECREATED --sort-order DESC --query 'data[0].id' --raw-output 2>/dev/null || true)
[ -z "$IMAGE_ID" ] || [ "$IMAGE_ID" = "null" ] && IMAGE_ID=$(oci compute image list -c $COMPARTMENT_ID --operating-system "Canonical Ubuntu" --operating-system-version "22.04 Minimal aarch64" --shape VM.Standard.A1.Flex --sort-by TIMECREATED --sort-order DESC --query 'data[0].id' --raw-output)
echo "IMAGE=$IMAGE_ID"; [ -z "$IMAGE_ID" ] || [ "$IMAGE_ID" = "null" ] && { echo "No aarch64 image found"; exit 1; }

# ---------- network (reuse if exists) ----------
VCN_ID=$(oci network vcn list -c $COMPARTMENT_ID --display-name $VCN_NAME --query 'data[0].id' --raw-output 2>/dev/null || true)
[ -z "$VCN_ID" ] || [ "$VCN_ID" = "null" ] && VCN_ID=$(oci network vcn create -c $COMPARTMENT_ID --cidr-blocks '["10.0.0.0/16"]' --display-name $VCN_NAME --query 'data.id' --raw-output)
echo "VCN=$VCN_ID"
IGW_ID=$(oci network internet-gateway list -c $COMPARTMENT_ID --vcn-id $VCN_ID --query 'data[0].id' --raw-output 2>/dev/null || true)
[ -z "$IGW_ID" ] || [ "$IGW_ID" = "null" ] && IGW_ID=$(oci network internet-gateway create -c $COMPARTMENT_ID --is-enabled true --vcn-id $VCN_ID --display-name saas-igw --query 'data.id' --raw-output)
RT_ID=$(oci network route-table list -c $COMPARTMENT_ID --vcn-id $VCN_ID --display-name saas-rt --query 'data[0].id' --raw-output 2>/dev/null || true)
[ -z "$RT_ID" ] || [ "$RT_ID" = "null" ] && RT_ID=$(oci network route-table create -c $COMPARTMENT_ID --vcn-id $VCN_ID --display-name saas-rt --route-rules '[{"destination":"0.0.0.0/0","destinationType":"CIDR_BLOCK","networkEntityId":"'$IGW_ID'"}]' --query 'data.id' --raw-output)
SL_ID=$(oci network security-list list -c $COMPARTMENT_ID --vcn-id $VCN_ID --display-name saas-sl --query 'data[0].id' --raw-output 2>/dev/null || true)
[ -z "$SL_ID" ] || [ "$SL_ID" = "null" ] && SL_ID=$(oci network security-list create -c $COMPARTMENT_ID --vcn-id $VCN_ID --display-name saas-sl --egress-security-rules '[{"destination":"0.0.0.0/0","protocol":"all"}]' --ingress-security-rules '[{"source":"0.0.0.0/0","protocol":"6","tcpOptions":{"destinationPortRange":{"min":80,"max":80}}},{"source":"0.0.0.0/0","protocol":"6","tcpOptions":{"destinationPortRange":{"min":443,"max":443}}},{"source":"0.0.0.0/0","protocol":"6","tcpOptions":{"destinationPortRange":{"min":22,"max":22}}}]' --query 'data.id' --raw-output)
SUBNET_ID=$(oci network subnet list -c $COMPARTMENT_ID --vcn-id $VCN_ID --display-name $SUBNET_NAME --query 'data[0].id' --raw-output 2>/dev/null || true)
[ -z "$SUBNET_ID" ] || [ "$SUBNET_ID" = "null" ] && SUBNET_ID=$(oci network subnet create -c $COMPARTMENT_ID --vcn-id $VCN_ID --cidr-block 10.0.0.0/24 --display-name $SUBNET_NAME --route-table-id $RT_ID --security-list-ids '["'$SL_ID'"]' --prohibit-public-ip-on-vnic false --query 'data.id' --raw-output)
echo "SUBNET=$SUBNET_ID"

# ---------- instance (try ADs in order, A1 often out of capacity) ----------
# NOTE: network + key above are reused on re-run; only this block retries.
# Hyderabad A1 is frequently "Out of host capacity" - script tries 2/12 then 1/6 fallback.
INSTANCE_ID=""
for SHAPE_CFG in '{"ocpus":2,"memoryInGBs":12}' '{"ocpus":1,"memoryInGBs":6}'; do
  echo "Trying shape-config $SHAPE_CFG..."
  for AD in $(oci iam availability-domain list -c $COMPARTMENT_ID | jq -r '.data[].name'); do
    echo "Trying $AD..."
    ERR=/tmp/launch-err.txt
    if INSTANCE_ID=$(oci compute instance launch -c $COMPARTMENT_ID --availability-domain "$AD" --display-name $NAME --shape VM.Standard.A1.Flex --shape-config "$SHAPE_CFG" --image-id $IMAGE_ID --subnet-id $SUBNET_ID --assign-public-ip true --ssh-authorized-keys-file /tmp/saas-key.pub --boot-volume-size-in-gbs 100 --query 'data.id' --raw-output 2>"$ERR"); then
      [[ "$INSTANCE_ID" == ocid* ]] && { echo "Launched in $AD $SHAPE_CFG: $INSTANCE_ID"; break 2; } || { cat "$ERR"; INSTANCE_ID=""; }
    else cat "$ERR"; echo "---"; INSTANCE_ID=""; fi
  done
done
[[ "$INSTANCE_ID" == ocid* ]] || { echo "All ADs out of capacity. Options: 1) re-run later (capacity frees early-morning UTC), 2) switch region: oci cli --region us-ashburn-1|ap-mumbai-1 and re-run with fresh VCN, 3) paid E4 Flex as last resort."; exit 1; }
oci compute instance get --instance-id $INSTANCE_ID --query 'data."lifecycle-state"' --wait-for-state RUNNING --wait-interval-seconds 10 >/dev/null
PUB_IP=$(oci compute instance list-vnics --instance-id $INSTANCE_ID --query 'data[0]."public-ip"' --raw-output)
echo "DONE Instance=$INSTANCE_ID PublicIP=$PUB_IP"
echo "--- DOWNLOAD private key NOW (Cloud Shell gear -> Download -> /tmp/saas-key) ---"
echo "SSH: ssh -i saas-key ubuntu@$PUB_IP"
