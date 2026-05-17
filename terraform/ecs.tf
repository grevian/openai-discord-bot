resource "aws_ecs_cluster" "main" {
  name = local.name_prefix

  setting {
    name  = "containerInsights"
    value = "enabled"
  }
}

resource "aws_ecs_cluster_capacity_providers" "main" {
  cluster_name       = aws_ecs_cluster.main.name
  capacity_providers = ["FARGATE", "FARGATE_SPOT"]
}

resource "aws_iam_role_policy" "task_exec" {
  name = "ecs-exec"
  role = aws_iam_role.task.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "ssmmessages:CreateControlChannel",
        "ssmmessages:CreateDataChannel",
        "ssmmessages:OpenControlChannel",
        "ssmmessages:OpenDataChannel",
      ]
      Resource = "*"
    }]
  })
}

resource "aws_ecs_task_definition" "service" {
  family                   = local.name_prefix
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = local.cpu
  memory                   = local.memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([
    {
      name      = local.service
      image     = "${aws_ecr_repository.service.repository_url}:${var.image_tag}"
      essential = true

      environment = [
        { name = "BOT_JSON_LOGS", value = "true" },
        { name = "BOT_TRACING", value = "true" },
        { name = "BOT_OTLP_LOGS", value = "true" },
        { name = "BOT_OPENAIDISCORDBOTIMAGES_NAME", value = aws_s3_bucket.images.id },
        { name = "AI_DISCORD_BOT_CONVERSATIONS_NAME", value = aws_dynamodb_table.conversations.id },
        { name = "OTEL_SERVICE_NAME", value = "danbot" },
        { name = "OTEL_EXPORTER_OTLP_INSECURE", value = "true" },
      ]

      secrets = [
        { name = "BOT_DISCORD_TOKEN", valueFrom = aws_ssm_parameter.discord_token.arn },
        { name = "BOT_OPENAI_AUTH_TOKEN", valueFrom = aws_ssm_parameter.openai_token.arn },
      ]

      mountPoints    = []
      portMappings   = []
      systemControls = []
      volumesFrom    = []

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.service.name
          awslogs-region        = data.aws_region.current.region
          awslogs-stream-prefix = "app"
        }
      }
    },
    {
      name      = "otel-sidecar"
      image     = "public.ecr.aws/aws-observability/aws-otel-collector:latest"
      essential = true

      environment = []

      secrets = [
        { name = "AOT_CONFIG_CONTENT", valueFrom = aws_ssm_parameter.otel_config.arn },
      ]

      mountPoints    = []
      portMappings   = []
      systemControls = []
      volumesFrom    = []

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.service.name
          awslogs-region        = data.aws_region.current.region
          awslogs-stream-prefix = "otel"
        }
      }
    },
  ])
}

resource "aws_ecs_service" "main" {
  name                   = local.service
  cluster                = aws_ecs_cluster.main.id
  task_definition        = aws_ecs_task_definition.service.arn
  desired_count          = var.desired_count
  enable_execute_command = true
  propagate_tags         = "SERVICE"

  capacity_provider_strategy {
    capacity_provider = "FARGATE_SPOT"
    weight            = 1
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.tasks.id]
    assign_public_ip = true
  }

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }
}
