variable "aws_region" {
  description = "AWS region for the VPC, ECS service, ALB, and S3 bucket. CloudFront is global."
  type        = string
}

variable "name" {
  description = "Short, DNS-safe deployment name used to name AWS resources."
  type        = string
  default     = "gobeyond"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}$", var.name))
    error_message = "name must start with a lowercase letter and contain only lowercase letters, numbers, and hyphens."
  }
}

variable "vpc_cidr" {
  description = "CIDR assigned to the reference VPC."
  type        = string
  default     = "10.42.0.0/16"
}

variable "availability_zones" {
  description = "Two or more available zones in aws_region. The caller supplies these to make subnet selection deterministic."
  type        = list(string)

  validation {
    condition     = length(var.availability_zones) >= 2
    error_message = "CloudFront VPC origins and an ALB require at least two availability zones."
  }
}

variable "private_subnet_cidrs" {
  description = "Private subnet CIDRs, one per availability zone. ECS tasks and the internal ALB live here."
  type        = list(string)

  validation {
    condition     = length(var.private_subnet_cidrs) == length(var.availability_zones)
    error_message = "private_subnet_cidrs must have one item for every availability zone."
  }
}

variable "public_subnet_cidrs" {
  description = "Public subnet CIDRs, one per availability zone. NAT gateways use these subnets when enabled."
  type        = list(string)

  validation {
    condition     = length(var.public_subnet_cidrs) == length(var.availability_zones)
    error_message = "public_subnet_cidrs must have one item for every availability zone."
  }
}

variable "create_nat_gateway" {
  description = "Create one NAT gateway so private Fargate tasks can pull images and write CloudWatch logs. Disable only when equivalent VPC endpoints/egress already exist."
  type        = bool
  default     = true
}

variable "server_image" {
  description = "Immutable OCI image URI for the Node-free GoBeyond server. Pin a digest for production."
  type        = string

  validation {
    condition     = length(trimspace(var.server_image)) > 0
    error_message = "server_image is required."
  }
}

variable "container_port" {
  description = "HTTP port exposed by the Go server image."
  type        = number
  default     = 8080
}

variable "cpu" {
  description = "Fargate CPU units for one task."
  type        = number
  default     = 512
}

variable "memory" {
  description = "Fargate memory in MiB for one task."
  type        = number
  default     = 1024
}

variable "desired_count" {
  description = "Initial healthy task count. Keep at least two for production availability."
  type        = number
  default     = 2
}

variable "min_count" {
  description = "Minimum number of healthy ECS tasks."
  type        = number
  default     = 2
}

variable "max_count" {
  description = "Maximum number of ECS tasks selected by target tracking."
  type        = number
  default     = 10
}

variable "cpu_architecture" {
  description = "Fargate architecture. Build and publish the matching OCI image; valid values are ARM64 and X86_64."
  type        = string
  default     = "ARM64"

  validation {
    condition     = contains(["ARM64", "X86_64"], var.cpu_architecture)
    error_message = "cpu_architecture must be ARM64 or X86_64."
  }
}

variable "public_origin" {
  description = "Canonical public HTTPS origin, for example https://www.example.com. The Go server must use this rather than an inbound Host header for canonical metadata."
  type        = string

  validation {
    condition     = can(regex("^https://", var.public_origin))
    error_message = "public_origin must be an HTTPS URL."
  }
}

variable "aliases" {
  description = "Optional CloudFront aliases. Supply a us-east-1 ACM certificate when this is non-empty."
  type        = list(string)
  default     = []
}

variable "acm_certificate_arn" {
  description = "Optional us-east-1 ACM certificate for aliases. Leave null to use the CloudFront domain name."
  type        = string
  default     = null
}

variable "static_route_paths" {
  description = "Exact public paths whose prebuilt documents live in S3, including / when the root page is static. Feed this from the generated route manifest during release preparation."
  type        = list(string)
  default     = ["/", "/robots.txt", "/sitemap.xml"]

  validation {
    condition     = alltrue([for path in var.static_route_paths : startswith(path, "/") && !startswith(path, "/_gobeyond/")])
    error_message = "static_route_paths must begin with / and may not claim the reserved /_gobeyond/ namespace."
  }
}

variable "public_asset_paths" {
  description = "Exact files copied from public/ and served from S3, normally populated from dist/deploy/route-trie.json staticAssetPaths."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for path in var.public_asset_paths : startswith(path, "/") && !endswith(path, "/") && !startswith(path, "/_gobeyond/")])
    error_message = "public_asset_paths must be exact absolute paths outside the reserved /_gobeyond/ namespace."
  }
}

variable "static_route_prefixes" {
  description = "Public path prefixes whose prebuilt documents live in S3. Use only for wholly static subtrees, for example /marketing/."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for prefix in var.static_route_prefixes : startswith(prefix, "/") && endswith(prefix, "/") && !startswith(prefix, "/_gobeyond/")])
    error_message = "static_route_prefixes must begin and end with / and may not claim /_gobeyond/."
  }
}

variable "static_asset_retention_days" {
  description = "How long noncurrent, immutable S3 artifacts are retained. This is the MVP static-asset rollback window, not old Go service retention."
  type        = number
  default     = 14
}

variable "tags" {
  description = "Additional tags applied to all supported resources."
  type        = map(string)
  default     = {}
}
