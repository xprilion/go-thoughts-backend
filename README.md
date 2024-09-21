# Firestore Real-time Poll and Message Processor with Google AI

This Go application listens to new user messages and poll updates in Firestore, processes them in real-time, and responds with AI-generated messages using the Gemini model from Google AI. It also periodically sends status updates to a chat platform based on the current state of polls and conversations.

## Features

- **Real-time Firestore Integration**: Listens for new user messages and processes them immediately.
- **AI-Powered Responses**: Uses Google AI's Gemini model to generate responses based on the conversation summary.
- **Poll Monitoring**: Fetches poll status from Firestore and updates the conversation summary.
- **Concurrency**: Utilizes Go's `sync.WaitGroup` and `sync.Mutex` to ensure concurrent processes run safely.

## Prerequisites

- [Go](https://golang.org/doc/install) (1.16+)
- [Firestore](https://cloud.google.com/firestore/docs/client/get-firebase) database with proper collections set up.
- Service account credentials with access to your Firestore project.
- `.env` file with the necessary configuration.
- Firebase Admin SDK and Google AI Genkit SDK.

## Setup

### Firestore Collections

1. **User Messages**: This collection (`gccdpune-user`) stores incoming user messages.
2. **Poll Collection**: This collection (`gccdpune-poll`) contains poll questions and options.
3. **Ping Collection**: This collection (`gccdpune-go-pings`) stores the AI-generated responses.

### Environment Variables

Create a `.env` file in the root of your project:

```bash
# .env
SERVICE_ACCOUNT_PATH=".keys/serviceAccountKey.json"
```

### Firestore Document Schema

#### User Messages Collection (`gccdpune-user`):
- `id`: string (unique identifier)
- `message`: string (user's message)
- `timestamp`: timestamp (message creation time)
- `processed`: boolean (whether the message has been processed)

#### Poll Collection (`gccdpune-poll`):
- `question`: string (the poll question)
- `options`: map (keyed by option label, containing poll options with their text and voters)

## Installation

1. Clone this repository:

```bash
git clone https://github.com/your-username/firestore-ai-poll.git
cd firestore-ai-poll
```

2. Install the necessary Go modules:

```bash
go mod tidy
```

3. Run the application:

```bash
go run main.go
```

## How It Works

1. **Mark Existing Messages as Processed**: The program first scans and marks all existing unprocessed messages in the `gccdpune-user` collection as processed, so that only new messages are handled.
   
2. **Listen for New Messages**: The program listens for any new user messages and processes them by generating a response using the Gemini AI model.

3. **Poll Monitoring**: The app periodically checks the status of a poll in Firestore and generates a summary, which is then used to update the conversation summary.

4. **AI-Generated Responses**: When a new message arrives, the Gemini AI model generates a response, and it is stored in Firestore for display in the chat.

## Contributing

Feel free to fork this repository, create a new branch, and submit pull requests for any improvements or features you'd like to add.

## License

This project is licensed under the MIT License.