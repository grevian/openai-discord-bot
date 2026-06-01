data "aws_ssm_parameter" "al2023_arm64_ami" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

resource "aws_security_group" "instance" {
  name        = "${local.name_prefix}-instance"
  description = "Bot EC2 instance: egress only"
  vpc_id      = aws_vpc.main.id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_launch_template" "bot" {
  name_prefix            = "${local.name_prefix}-"
  image_id               = data.aws_ssm_parameter.al2023_arm64_ami.value
  instance_type          = var.instance_type
  update_default_version = true

  iam_instance_profile {
    arn = aws_iam_instance_profile.bot.arn
  }

  network_interfaces {
    associate_public_ip_address = true
    security_groups             = [aws_security_group.instance.id]
  }

  metadata_options {
    http_tokens                 = "required"
    http_endpoint               = "enabled"
    http_put_response_hop_limit = 2
  }

  user_data = base64encode(templatefile("${path.module}/user-data.sh.tftpl", {
    region            = data.aws_region.current.region
    service           = local.service
    environment       = local.environment
    image_repo        = local.image_repo
    images_bucket     = aws_s3_bucket.images.id
    conversations_tbl = aws_dynamodb_table.conversations.id
    otel_config       = file("${path.module}/../adot/otel-agent-config.yaml")
    otel_version      = var.otel_collector_version
  }))

  tag_specifications {
    resource_type = "instance"
    tags = {
      Name = local.name_prefix
      App  = local.service
    }
  }
}

resource "aws_autoscaling_group" "bot" {
  name                      = local.name_prefix
  desired_capacity          = 1
  min_size                  = 1
  max_size                  = 1
  vpc_zone_identifier       = aws_subnet.public[*].id
  health_check_type         = "EC2"
  health_check_grace_period = 120

  launch_template {
    id      = aws_launch_template.bot.id
    version = "$Latest"
  }

  instance_refresh {
    strategy = "Rolling"
    preferences {
      min_healthy_percentage = 0
      instance_warmup        = 60
    }
  }

  tag {
    key                 = "Name"
    value               = local.name_prefix
    propagate_at_launch = true
  }

  tag {
    key                 = "App"
    value               = local.service
    propagate_at_launch = true
  }
}
