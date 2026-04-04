FROM mcr.microsoft.com/devcontainers/base:ubuntu

# Install dependencies
RUN apt-get update && apt-get install -y \
    curl unzip git build-essential \
    && rm -rf /var/lib/apt/lists/*

# Install Docker CLI
RUN curl -fsSL https://get.docker.com | sh

# Install Task
RUN sh -c "$(curl --location https://taskfile.dev/install.sh)" -- -d -b /usr/local/bin

# Install Terraform
ARG TERRAFORM_VERSION=1.14.8
RUN curl -fsSL "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_amd64.zip" -o /tmp/terraform.zip \
    && unzip /tmp/terraform.zip -d /usr/local/bin \
    && rm /tmp/terraform.zip \
    && terraform --version