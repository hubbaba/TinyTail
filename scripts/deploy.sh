#!/bin/bash
set -e

# TinyTail Deployment Script
# Generates secrets and deploys with SAM

echo "================================================"
echo "  TinyTail Deployment Script"
echo "================================================"
echo ""

SECRETS_FILE=".secrets"

# Load region, stack name, and profile from secrets file if it exists
if [ -f "$SECRETS_FILE" ]; then
    source "$SECRETS_FILE"
fi

# Set defaults if not loaded from secrets
REGION="${AWS_REGION:-}"
STACK_NAME="${STACK_NAME:-tinytail}"
PROFILE="${AWS_PROFILE:-}"

# Function to generate a secure random string
generate_secret() {
    local length=$1
    openssl rand -base64 $length | tr -d "=+/" | cut -c1-$length
}

# Check if secrets file exists
if [ -f "$SECRETS_FILE" ]; then
    # Automatically use existing secrets
    source "$SECRETS_FILE"
    echo "✓ Using existing secrets from $SECRETS_FILE"
else
    echo "No existing secrets found. Generating new secrets..."
    INGEST_SECRET=$(generate_secret 43)
    UI_PASSWORD=$(generate_secret 24)

    # Prompt for region
    echo ""
    read -p "AWS Region [us-east-2]: " input_region
    REGION=${input_region:-us-east-2}
    echo ""
    read -p "Stack Name [tinytail]: " input_stack
    STACK_NAME=${input_stack:-tinytail}
    echo ""
    read -p "AWS Profile [tinytail]: " input_profile
    PROFILE=${input_profile:-tinytail}

    # Save to file
    cat > "$SECRETS_FILE" << 'EOF'
# TinyTail Secrets - DO NOT COMMIT TO GIT!
# Generated: $(date)
AWS_REGION=us-east-2
AWS_PROFILE=tinytail
STACK_NAME=tinytail
INGEST_SECRET=<your-secret-here>
UI_PASSWORD=<your-password-here>

# SES Configuration (for email alerts)
ALERT_FROM_EMAIL=alerts@example.com

# Alert Rules (JSON array - must be single line!)
# To disable alerts, use: ALERT_RULES='[]'
# Example with alerts enabled:
ALERT_RULES='[{"pattern":"Exception occurred while constructing Stripe event","window":"10m","email":"your@work.com"},{"pattern":"Exception","window":"24h","email":"your@work.com"},{"pattern":"Could not initialize framework within the 20000ms timeout","window":"10m","email":"your@work.com"}]'
EOF
    chmod 600 "$SECRETS_FILE"
    echo "✓ Template .secrets file created"
    echo ""
    echo "Please edit $SECRETS_FILE to add your actual secrets and alert rules"
    exit 1
fi

# Set defaults for optional parameters
ALERT_RULES="${ALERT_RULES:-[]}"
ALERT_FROM_EMAIL="${ALERT_FROM_EMAIL:-}"

echo ""
echo "================================================"
echo "  Your TinyTail Configuration"
echo "================================================"
echo ""
echo "AWS Region:    $REGION"
echo "AWS Profile:   $PROFILE"
echo "Stack Name:    $STACK_NAME"
echo ""
echo "UI Password (for web login):"
echo "  $UI_PASSWORD"
echo ""
echo "Ingest Secret (for logback-appender):"
echo "  $INGEST_SECRET"
echo ""
echo "IMPORTANT: Save these somewhere safe!"
echo "They are also stored in: $SECRETS_FILE"
echo ""
echo "================================================"
echo ""

# Create S3 bucket for deployments if it doesn't exist
S3_BUCKET="tinytail-deployments-${AWS::AccountId}"
echo ""
echo "Checking for deployment S3 bucket..."
if ! aws s3 ls "s3://tinytail-deployments" --region "$REGION" --profile "$PROFILE" &>/dev/null; then
    echo "Creating S3 bucket: tinytail-deployments"
    aws s3 mb "s3://tinytail-deployments" --region "$REGION" --profile "$PROFILE"
    echo "✓ S3 bucket created"
else
    echo "✓ S3 bucket exists: tinytail-deployments"
fi

echo ""
echo "Generating alert rules config file..."
echo "$ALERT_RULES" > lambda/alert-rules.json
echo "✓ Created lambda/alert-rules.json"

echo ""
echo "Running SAM build..."
cd infrastructure
sam build

echo ""
echo "Deploying to AWS..."

sam deploy \
    --stack-name "$STACK_NAME" \
    --region "$REGION" \
    --profile "$PROFILE" \
    --capabilities CAPABILITY_IAM \
    --parameter-overrides "IngestSecret=$INGEST_SECRET" "UIPassword=$UI_PASSWORD" "AlertFromEmail=$ALERT_FROM_EMAIL" \
    --s3-bucket tinytail-deployments \
    --s3-prefix "$STACK_NAME"

cd ..

echo ""
echo "================================================"
echo "  Deployment Complete!"
echo "================================================"
echo ""

echo "Getting deployment info..."

# Extract URLs from stack outputs
API_URL=$(aws cloudformation describe-stacks \
    --stack-name "$STACK_NAME" \
    --region "$REGION" \
    --profile "$PROFILE" \
    --query 'Stacks[0].Outputs[?OutputKey==`ApiEndpoint`].OutputValue' \
    --output text)

UI_URL=$(aws cloudformation describe-stacks \
    --stack-name "$STACK_NAME" \
    --region "$REGION" \
    --profile "$PROFILE" \
    --query 'Stacks[0].Outputs[?OutputKey==`LogViewerUrl`].OutputValue' \
    --output text)

echo ""
echo "================================================"
echo "  TinyTail Web UI"
echo "================================================"
echo ""
echo "URL:      $UI_URL"
echo "Password: $UI_PASSWORD"
echo ""

echo "================================================"
echo "  Logback Configuration (copy/paste ready)"
echo "================================================"
echo ""
echo "<appender name=\"CONSOLE\" class=\"ch.qos.logback.core.ConsoleAppender\">"
echo "  <encoder>"
echo "    <pattern>%d{HH:mm:ss.SSS} [%thread] %-5level %logger{36} - %msg%n</pattern>"
echo "  </encoder>"
echo "</appender>"
echo ""
echo "<appender name=\"TINYTAIL\" class=\"com.tinytail.logback.TinyTailAppender\">"
echo "  <endpoint>${API_URL}logs/ingest</endpoint>"
echo "  <source>my-app</source>"
echo "  <secret>$INGEST_SECRET</secret>"
echo "</appender>"
echo ""
echo "<root level=\"INFO\">"
echo "  <appender-ref ref=\"CONSOLE\" />"
echo "  <appender-ref ref=\"TINYTAIL\" />"
echo "</root>"
echo ""
echo "================================================"
echo ""