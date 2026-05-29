locals {
  name_prefix = "${var.project_name}-${var.environment}"

  service_name           = coalesce(var.service_name, local.name_prefix)
  artifact_repository_id = coalesce(var.artifact_repository_id, local.name_prefix)
  runtime_service_account_id = substr(
    replace("${lower(var.project_name)}-${lower(var.environment)}", "_", "-"),
    0,
    30,
  )

  labels = merge(
    {
      project     = lower(var.project_name)
      environment = lower(var.environment)
      managed_by  = "terraform"
      repository  = "cortex"
    },
    var.gcp_labels
  )
}
