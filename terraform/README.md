# Terraform

Minimal GCP Terraform scaffold for `cortex`.

## Layout

- `versions.tf`: Terraform and Google provider version constraints
- `backend.tf`: remote state backend declaration for GCS
- `backend.hcl.example`: example GCS backend configuration values
- `providers.tf`: Google provider configuration
- `variables.tf`: input variables for project, region, image, scaling, and labels
- `locals.tf`: shared naming and label conventions
- `data.tf`: Google Cloud project metadata lookups
- `main.tf`: API enablement, Artifact Registry, Secret Manager, IAM, and Cloud Run
- `outputs.tf`: useful deployment outputs
- `terraform.tfvars.example`: example local variables file
- `environments/*.tfvars`: per-environment variable files
- `modules/`: shared child modules as the stack grows

## Quick Start

1. Copy `backend.hcl.example` to `backend.hcl` and fill in your state bucket details.
2. Authenticate locally with `gcloud auth application-default login`.
3. Choose an environment file from `environments/` or copy `terraform.tfvars.example`.
4. Build and push the Cortex container image referenced by `container_image`.
5. Run:

```bash
terraform init -backend-config=backend.hcl
terraform plan -var-file=environments/dev.tfvars
terraform apply -var-file=environments/dev.tfvars
```

## Resources

The current scaffold provisions:

- Cloud Run for the Cortex runtime
- Artifact Registry for container images
- Secret Manager secrets for Slack credentials
- A runtime service account
- Baseline GCP service enablement
