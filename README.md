# openai-discord-bot

This is a silly discord to openai bot designed to amuse myself and my friends

## Interacting with the bot

In most circumstances you can just `@Danbot` and recieve a response, if you include 🧵 in your message the response will be in a thread, which will retain context between messages, though you still have to `@Danbot` inside the thread to get responses

![conversational interactions](https://user-images.githubusercontent.com/603334/230736019-528a4a65-f787-4a16-918f-43c2f0203ddd.png)

The bot also supports requests to `@Danbot draw me a picture of <something>` which will respond with a Dall-E generated picture as requested

![drawing interactions](https://user-images.githubusercontent.com/603334/230735886-4d869e36-919b-4f1c-8fab-3cfe5e6cf0cc.png)

## Running Locally

It's probably easiest to run this via the Dockerfile, just remember to set the 
necessary environment variables on startup 

```
docker build -t openaibot . && docker run --rm -it openaibot \
 -e BOT_TRACING=false \ 
 -e BOT_JSON_LOGS=false \
 -e BOT_OPENAI_AUTH_TOKEN=<redacted> \
 -e BOT_DISCORD_TOKEN=<redacted> \
 -e BOT_OPENAIDISCORDBOTIMAGES_NAME=<s3 bucket> \
 -e AI_DISCORD_BOT_CONVERSATIONS_NAME=<dynamodb_table_name>
```

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

Sometimes the bot ends up in a discord role with the same name as a the bot, which makes @ messages directed at it quite confusing, after adding the bot I'd suggest going to your server settings, and looking for a role with the same name as the bot user, if you see it, rename it something else like "GPTChatBot"
