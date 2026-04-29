# IAM setup for Route 53 DNS management

## What this is

Each bare metal node needs AWS credentials to upsert one A record in
Route 53. This document explains how to create an IAM user with the
minimum possible permissions.

## Blast radius analysis

If these credentials are compromised, an attacker can:
- ✅ Upsert/delete A records in the specific hosted zone
- ✅ List records in the zone
- ✅ Check change status

An attacker CANNOT:
- ❌ Access any other AWS service
- ❌ Modify any other hosted zone
- ❌ Read EC2 instances, S3, secrets, etc.
- ❌ Create or delete the hosted zone itself

This is the smallest viable IAM surface for this use case.

## Setup

### 1. Create the IAM policy

```bash
aws iam create-policy \
  --policy-name eth-node-operator-route53 \
  --policy-document file://deploy/iam/route53-dns-policy.json \
  --description "Allows eth-node-operator to manage A records in validators.example.com zone"
```

Edit `route53-dns-policy.json` first: replace `REPLACE_WITH_YOUR_ZONE_ID`
with your actual hosted zone ID (format: `Z1234567890ABC`).

### 2. Create a dedicated IAM user (one per cluster, not per node)

```bash
aws iam create-user --user-name eth-node-operator-dns

aws iam attach-user-policy \
  --user-name eth-node-operator-dns \
  --policy-arn arn:aws:iam::<ACCOUNT_ID>:policy/eth-node-operator-route53
```

### 3. Create an access key

```bash
aws iam create-access-key --user-name eth-node-operator-dns
```

Output:
```json
{
  "AccessKey": {
    "AccessKeyId": "AKIA...",
    "SecretAccessKey": "...",
    "Status": "Active"
  }
}
```

Store these in `/opt/eth-observability/.env` (not in version control):
```
AWS_ACCESS_KEY_ID=AKIA...
AWS_SECRET_ACCESS_KEY=...
AWS_DEFAULT_REGION=us-east-1
```

The Ansible bootstrap enforces `chmod 600` on this file.

### 4. Set up key rotation (recommended)

IAM access keys should be rotated every 90 days.

```bash
# Create new key
aws iam create-access-key --user-name eth-node-operator-dns

# Update .env on each node with new key
# Test DNS registration still works
# ethctl dns list --zone-id Z... --zone validators.example.com

# Delete old key
aws iam delete-access-key \
  --user-name eth-node-operator-dns \
  --access-key-id AKIA_OLD_KEY
```

### 5. Optional: use IAM Roles Anywhere for keyless auth

If your OVH nodes have X.509 certificates from a PKI you control,
IAM Roles Anywhere lets you get temporary credentials without long-lived
access keys. This is the gold standard but requires PKI infrastructure.
See: https://docs.aws.amazon.com/rolesanywhere/latest/userguide/introduction.html
