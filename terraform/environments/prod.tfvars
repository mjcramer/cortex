project_id      = "your-gcp-project-id"
project_name    = "cortex"
environment     = "prod"
gcp_region      = "us-west1"
container_image = "us-west1-docker.pkg.dev/your-gcp-project-id/cortex-prod/cortex:latest"

gcp_labels = {
  owner = "platform"
}
