---
name: aws-infra
description: Change the AWS side of just-a-db safely — CloudFormation stack updates (security group, IAM role, log groups), EBS disk resize, Route 53 records, S3 backups bucket. Use before touching cloudformation.yaml or anything in the AWS account.
---

# AWS infrastructure

Account 579438481493, region **us-east-1**. Stack `just-a-db` (from
`cloudformation.yaml`): the EC2 instance, its security group, Elastic IP,
instance role and the CloudWatch log groups. Parameters: VpcId, SubnetId,
BackupCronExpression, ResourcesBucketName (= `just-a-db-backups`).

Credentials: the laptop has none by default. The user runs `! aws login`
(it signs in as the account's root user); remind them to `aws logout` when
done. Their local IAM user `just-a-bd-local-user` only reaches the local test
bucket `just-a-db-backups-local`.

## Stack updates: always through a change set

Changing `ImageId`, `UserData`, `InstanceType`… **replaces the instance**, and
the data lives on its root disk: replacing it loses every database. So never
`update-stack` blind:

```shell
aws cloudformation create-change-set --region us-east-1 --stack-name just-a-db \
  --change-set-name <name> --template-body file://cloudformation.yaml \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameters ParameterKey=VpcId,UsePreviousValue=true \
    ParameterKey=SubnetId,UsePreviousValue=true \
    ParameterKey=BackupCronExpression,UsePreviousValue=true \
    ParameterKey=ResourcesBucketName,UsePreviousValue=true
aws cloudformation wait change-set-create-complete --region us-east-1 --change-set-name <arn>
aws cloudformation describe-change-set --region us-east-1 --change-set-name <arn> \
  --query "Changes[].ResourceChange.[Action,LogicalResourceId,Replacement]" --output text
```

Execute (`execute-change-set` + `wait stack-update-complete`) only if
`Instance` isn't in the list and nothing says `Replacement: True`. Security
group and IAM policy changes apply live, without restarts. Replacing the
instance (AL2 is EOL) is a planned migration: see docs/UPGRADE.md, "Later:
replacing the instance".

## Disk (EBS) resize

Root volume `vol-06b408557d762d046` (`/dev/xvda`, XFS). Done once already
(8 GB gp2 → 20 GB gp3, 2026-10-12, snapshot `snap-0df7d6b9d1f0adb43`). Online,
no downtime:

1. `aws ec2 create-snapshot --volume-id vol-06b408557d762d046 …` and
   `aws ec2 wait snapshot-completed`.
2. `aws ec2 modify-volume --volume-id vol-06b408557d762d046 --size <GB> --volume-type gp3`
   (wait for state `optimizing`; one modification per 6 hours, grow only).
3. `./connect.sh 'sudo growpart /dev/nvme0n1 1 && sudo xfs_growfs -d / && df -h /'`.

## DNS

Route 53 hosted zone `sanninlabs.org` (Z09887261J6UHRYXPY7A7): `db.sanninlabs.org`
A → 44.207.50.93 (the panel, and the host apps use for pgbouncer). The zone
also has Brevo's verification/DKIM records (the panel sends from
`justadb@sanninlabs.org`) and `payments` (wompi-router on Vercel): don't touch them.

## Backups bucket

`s3://just-a-db-backups/<database>/backup_<db>_<UTC timestamp>.dump` (pg_dump
custom format; older ones `.sql.gz`), roles in `_globals/`. 14 kept per
database.
