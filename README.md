# openai-discord-bot

This is a silly discord to openai bot designed to amuse myself and my friends

## Deployment

This bot uses the unfortunately named [AWS Copilot](https://aws.github.io/copilot-cli/docs/overview/) framework to deploy a simple docker service to ECS

It requires an OpenAI API Key, and a Discord Bot Token be set to `BOT_OPENAI_AUTH_TOKEN` and `BOT_DISCORD_TOKEN` respectively, probably in SSM,
 when deployed you should probably set `BOT_JSON_LOGS=true`


```
copilot env init               # Create a deployment environment, "test" by default
copilot secret init            # Create an SSM secret for BOT_OPENAI_AUTH_TOKEN
copilot secret init            # Create an SSM secret for BOT_DISCORD_TOKEN
copilot deploy --env test      # Deploy the application to ECS
```

## Attaching to Discord

You can authorize the currently deployed bot to your server with the following OAuth2 URL: https://discord.com/api/oauth2/authorize?client_id=1076924748124143727&permissions=395204176896&scope=bot
