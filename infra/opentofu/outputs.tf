output "cloudfront_domain_name" {
  description = "CloudFront hostname. Create DNS records or aliases outside this reference."
  value       = aws_cloudfront_distribution.this.domain_name
}

output "cloudfront_distribution_id" {
  description = "CloudFront distribution ID for release verification and cache invalidation review."
  value       = aws_cloudfront_distribution.this.id
}

output "cache_endpoint" {
  description = "Private TLS endpoint for the optional ElastiCache Serverless cache, or null when disabled."
  value       = var.create_cache ? aws_elasticache_serverless_cache.app[0].endpoint[0].address : null
}

output "cache_port" {
  description = "TLS port for the optional ElastiCache Serverless cache, or null when disabled."
  value       = var.create_cache ? aws_elasticache_serverless_cache.app[0].endpoint[0].port : null
}

output "static_bucket_name" {
  description = "Private S3 bucket receiving immutable browser assets, static data, and static documents."
  value       = aws_s3_bucket.static.id
}

output "ecs_cluster_name" {
  description = "ECS cluster containing the Go-only runtime service."
  value       = aws_ecs_cluster.this.name
}

output "ecs_service_name" {
  description = "ECS service serving dynamic documents, APIs, actions, and runtime navigation payloads."
  value       = aws_ecs_service.app.name
}

output "private_alb_dns_name" {
  description = "Private ALB DNS name. This is an origin-only endpoint, not a public application URL."
  value       = aws_lb.app.dns_name
}
