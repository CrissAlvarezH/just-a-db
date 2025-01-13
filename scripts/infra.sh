#!/bin/bash

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$DIR/base.sh"


function setup_stack() {
  source "$DIR/../.env"

  if [ -z "$VPC_ID" ]; then
    log "VPC_ID is not set in .env file, it will take the default vpc if exists" "warn"
    VPC_ID=$(aws ec2 describe-vpcs --filters Name=is-default,Values=true --query 'Vpcs[0].VpcId' --output text)
    if [ -z "$VPC_ID" ]; then
      log "No default VPC found, please set the VPC_ID in .env file" "error"
      exit 1
    fi
  fi

  if [ -z "$SUBNET_ID" ]; then
    log "SUBNET_ID is not set in .env file, it will take a random subnet of the default vpc" "warn"
    SUBNET_ID=$(aws ec2 describe-subnets --filters Name=vpc-id,Values=$VPC_ID --query 'Subnets[0].SubnetId' --output text)
    if [ -z "$SUBNET_ID" ]; then
      log "No subnet found in the default VPC, please set the SUBNET_ID in .env file" "error"
      exit 1
    fi
  fi

  if [ -z "$S3_BUCKET" ]; then
    log "S3_BUCKET is not set in .env file, please set the S3_BUCKET in .env file" "error"
    exit 1
  fi

  log "Validate if bucket exists"
  if ! aws s3 ls s3://$S3_BUCKET &>/dev/null; then
    log "Bucket $S3_BUCKET does not exist, please create it first" "error"
    exit 1
  fi

  log "Creating key pair"
  aws ec2 create-key-pair --key-name just-a-db --query 'KeyMaterial' --output text > just-a-db.pem
  chmod 400 just-a-db.pem

  log "INFO:"
  echo "region: $AWS_REGION"
  echo "vpc: $VPC_ID"
  echo "subnet: $SUBNET_ID"
  echo "bucket: $S3_BUCKET"
  echo "backup cron: $BACKUP_CRON_EXPRESSION"

  log "Creating stack just-a-db"
  aws cloudformation \
    create-stack \
    --stack-name just-a-db \
    --region $AWS_REGION \
    --template-body file://cloudformation.yaml \
    --parameters \
      ParameterKey=VpcId,ParameterValue="$VPC_ID" \
      ParameterKey=SubnetId,ParameterValue="$SUBNET_ID" \
      ParameterKey=BackupCronExpression,ParameterValue="$BACKUP_CRON_EXPRESSION" \
      ParameterKey=ResourcesBucketName,ParameterValue="$S3_BUCKET" \
    --capabilities CAPABILITY_NAMED_IAM |
    cat 
}


function get_stack_status() {
  while true; do
    clear
    log "... Getting stack status <Ctrl+C to stop>"

    status=$(aws cloudformation describe-stacks \
      --stack-name just-a-db \
      --region $AWS_REGION \
      --query "Stacks[0].StackStatus" \
      --output text)

    log "Stack status: $status"
    sleep 2
  done
}


function destroy_stack() {
  log "Deleting key pair"
  aws ec2 delete-key-pair --key-name just-a-db | cat

  log "Emptying bucket"
  aws s3 rm s3://just-a-db-backups --recursive | cat

  log "Deleting stack"
  aws cloudformation delete-stack --stack-name just-a-db --region $AWS_REGION | cat

  log "Cleaning up"
  rm -f just-a-db.pem
}

function get_instance_ip() {
  ip=$(aws cloudformation describe-stacks \
    --stack-name just-a-db \
    --region $AWS_REGION \
    --query "Stacks[0].Outputs[?OutputKey=='InstancePublicIp'].OutputValue" \
    --output text)

  echo $ip
}


function connect_to_instance() {
  ip=$(get_instance_ip)
  ssh -o StrictHostKeyChecking=no -i just-a-db.pem ec2-user@$ip
}


function get_user_data_output() {
  ip=$(get_instance_ip)

  ssh -o StrictHostKeyChecking=no -i just-a-db.pem ec2-user@$ip \
    "sudo cat /var/log/cloud-init-output.log" > user_data_output.log

  echo "User data output:"
  cat user_data_output.log
}
