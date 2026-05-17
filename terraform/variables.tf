variable "instance_type" {
  description = "EC2 instance type for the bot. ARM/Graviton."
  type        = string
  default     = "t4g.nano"
}
