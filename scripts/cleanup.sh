#!/bin/bash
set -e

# TinyTail Cleanup Script
# Safely removes all AWS resources deployed by TinyTail

echo "================================================"
echo "  TinyTail Cleanup Script"
echo "================================================"
echo ""

SECRETS_FILE=".secrets"
STACK_NAME="${STACK_NAME:-tinytail}"

# Try to load region and profile from secrets file
if [ -f "$SECRETS_FILE" ]; then
    source "$SECRETS_FILE"
fi
REGION="${AWS_REGION:-us-east-1}"
PROFILE="${AWS_PROFILE:-}"

echo "This will DELETE the following resources:"
echo ""
echo "  Region:      $REGION"
echo "  Profile:     ${PROFILE:-default}"
echo "  Stack Name:  $STACK_NAME"
echo ""
echo "Resources to be deleted:"
echo "  • Lambda function (TinyTailFunction)"
echo "  • DynamoDB tables (TinyTailLogs, TinyTailSessions)"
echo "  • API Gateway"
echo "  • IAM roles"
echo "  • ALL LOG DATA will be permanently deleted!"
echo ""

# Check if stack exists
echo "Checking if stack exists..."
PROFILE_ARG=""
if [ -n "$PROFILE" ]; then
    PROFILE_ARG="--profile $PROFILE"
fi

if ! aws cloudformation describe-stacks \
    --stack-name "$STACK_NAME" \
    --region "$REGION" \
    $PROFILE_ARG \
    --query 'Stacks[0].StackName' \
    --output text &>/dev/null; then
    echo ""
    echo "⚠️  Stack '$STACK_NAME' not found in region $REGION"
    echo "Nothing to delete."
    exit 0
fi

echo "✓ Stack found"
echo ""

# Show current stack outputs before deletion
echo "Current stack outputs:"
aws cloudformation describe-stacks \
    --stack-name "$STACK_NAME" \
    --region "$REGION" \
    $PROFILE_ARG \
    --query 'Stacks[0].Outputs' \
    --output table 2>/dev/null || echo "  (no outputs available)"

echo ""
echo "================================================"
echo "  ⚠️  WARNING: This action cannot be undone!"
echo "================================================"
echo ""
read -p "Are you sure you want to delete everything? (yes/no): " confirm

if [ "$confirm" != "yes" ]; then
    echo "Cleanup cancelled."
    exit 0
fi

echo ""
echo "Deleting CloudFormation stack..."
aws cloudformation delete-stack \
    --stack-name "$STACK_NAME" \
    --region "$REGION" \
    $PROFILE_ARG

echo "✓ Delete initiated"
echo ""
echo "Waiting for stack deletion to complete..."
echo "(This may take 1-2 minutes. Press Ctrl+C to stop waiting, deletion will continue in background)"
echo ""

# Wait for deletion with timeout
aws cloudformation wait stack-delete-complete \
    --stack-name "$STACK_NAME" \
    --region "$REGION" \
    $PROFILE_ARG \
    2>/dev/null && echo "✓ Stack deleted successfully" || {
    echo ""
    echo "Note: Stack deletion is in progress but taking longer than expected."
    echo "Check status with:"
    echo "  aws cloudformation describe-stacks --stack-name $STACK_NAME --region $REGION"
}

echo ""
echo "================================================"
echo "  Cleanup Options"
echo "================================================"
echo ""

# Ask about SAM artifacts bucket
echo "Do you want to delete the SAM deployment artifacts bucket?"
echo "(This bucket contains deployment packages and CloudFormation templates)"
read -p "Delete SAM bucket? (yes/no) [no]: " delete_bucket
delete_bucket=${delete_bucket:-no}

if [ "$delete_bucket" = "yes" ]; then
    SAM_BUCKET=$(aws cloudformation describe-stacks \
        --stack-name aws-sam-cli-managed-default \
        --region "$REGION" \
        $PROFILE_ARG \
        --query 'Stacks[0].Outputs[?OutputKey==`SourceBucket`].OutputValue' \
        --output text 2>/dev/null || echo "")

    if [ -n "$SAM_BUCKET" ]; then
        echo "Emptying bucket: $SAM_BUCKET"
        aws s3 rm "s3://$SAM_BUCKET" --recursive --region "$REGION" $PROFILE_ARG 2>/dev/null || true
        echo "Deleting bucket: $SAM_BUCKET"
        aws s3 rb "s3://$SAM_BUCKET" --region "$REGION" $PROFILE_ARG 2>/dev/null || true
        echo "✓ SAM bucket deleted"
    else
        echo "No SAM bucket found or already deleted"
    fi
fi

echo ""

# Ask about local secrets file
if [ -f "$SECRETS_FILE" ]; then
    echo "Do you want to delete the local .secrets file?"
    echo "(File: $SECRETS_FILE)"
    echo "  • Contains: UI password and ingest secret"
    echo "  • You'll need to generate new secrets on next deployment"
    read -p "Delete .secrets file? (yes/no) [no]: " delete_secrets
    delete_secrets=${delete_secrets:-no}

    if [ "$delete_secrets" = "yes" ]; then
        rm -f "$SECRETS_FILE"
        echo "✓ Deleted $SECRETS_FILE"
    else
        echo "✓ Kept $SECRETS_FILE (can be reused for next deployment)"
    fi
fi

# Ask about build artifacts
echo ""
echo "Do you want to clean local build artifacts?"
echo "(Lambda bootstrap, .aws-sam folder)"
read -p "Clean build artifacts? (yes/no) [yes]: " clean_build
clean_build=${clean_build:-yes}

if [ "$clean_build" = "yes" ]; then
    rm -f lambda/bootstrap
    rm -rf infrastructure/.aws-sam
    echo "✓ Cleaned build artifacts"
fi

echo ""
echo "================================================"
echo "  Cleanup Complete!"
echo "================================================"
echo ""
echo "All TinyTail resources have been removed from AWS."
echo ""
echo "To redeploy, run:"
echo "  ./scripts/deploy.sh"
echo ""