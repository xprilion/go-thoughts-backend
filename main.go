package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go"
	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/plugins/googleai"
	"github.com/joho/godotenv"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Message struct {
	ID        string    `firestore:"id"`
	Message   string    `firestore:"message"`
	Timestamp time.Time `firestore:"timestamp"`
	Processed bool      `firestore:"processed"`
}

type PollOption struct {
	OpText string   `firestore:"text"`
	Label  string   `firestore:"label"`
	Voters []string `firestore:"voters"`
}

type PollQuestion struct {
	Question string                `firestore:"question"`
	Options  map[string]PollOption `firestore:"options"`
}

var (
	lastUserMessage     time.Time
	conversationSummary string
	lastResponseTime    time.Time
	mu                  sync.Mutex
	model               ai.Model
)

func main() {
	godotenv.Load()

	serviceAccountPath := ".keys/serviceAccountKey.json"
	userCollection := "gccdpune-user"
	pingCollection := "gccdpune-go-pings"
	pollCollection := "gccdpune-poll"

	ctx := context.Background()

	// Initialize Google AI once
	if err := googleai.Init(ctx, nil); err != nil {
		log.Fatalf("Error initializing Google AI: %v", err)
	}
	model = googleai.Model("gemini-1.5-flash")
	if model == nil {
		log.Fatalf("Could not find Gemini model")
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		err := markExistingMessagesAsProcessed(ctx, serviceAccountPath, userCollection)
		if err != nil {
			log.Fatalf("Error marking existing messages: %v", err)
		}

		err = listenForNewUserMessages(ctx, os.Stdout, serviceAccountPath, userCollection, pingCollection)
		if err != nil {
			log.Fatalf("Error listening for new user messages: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		err := monitorAndRespond(ctx, os.Stdout, serviceAccountPath, userCollection, pingCollection, pollCollection)
		if err != nil {
			log.Fatalf("Error monitoring and responding: %v", err)
		}
	}()

	wg.Wait()
}

// Mark all existing unprocessed messages as processed and skip them.
func markExistingMessagesAsProcessed(ctx context.Context, serviceAccountPath, userCollection string) error {
	sa := option.WithCredentialsFile(serviceAccountPath)
	app, err := firebase.NewApp(ctx, nil, sa)
	if err != nil {
		return fmt.Errorf("error initializing app: %w", err)
	}

	client, err := app.Firestore(ctx)
	if err != nil {
		return fmt.Errorf("error initializing Firestore: %w", err)
	}
	defer client.Close()

	iter := client.Collection(userCollection).Where("processed", "==", false).Documents(ctx)
	defer iter.Stop()

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("error iterating through unprocessed messages: %w", err)
		}

		// Mark the message as processed immediately
		_, err = doc.Ref.Update(ctx, []firestore.Update{
			{Path: "processed", Value: true},
		})
		if err != nil {
			return fmt.Errorf("error marking message as processed: %w", err)
		}

		fmt.Printf("Existing message marked as processed: %s\n", doc.Ref.ID)
	}

	return nil
}

// This function listens for only new incoming user messages (already processed messages are skipped).
func listenForNewUserMessages(ctx context.Context, w io.Writer, serviceAccountPath, userCollection, pingCollection string) error {
	sa := option.WithCredentialsFile(serviceAccountPath)
	app, err := firebase.NewApp(ctx, nil, sa)
	if err != nil {
		return fmt.Errorf("error initializing app: %w", err)
	}

	client, err := app.Firestore(ctx)
	if err != nil {
		return fmt.Errorf("error initializing Firestore: %w", err)
	}
	defer client.Close()

	// Listen for new unprocessed messages
	it := client.Collection(userCollection).Where("processed", "==", false).Snapshots(ctx)
	for {
		snap, err := it.Next()
		if status.Code(err) == codes.DeadlineExceeded {
			fmt.Fprintln(w, "Timeout reached")
			return nil
		}
		if err != nil {
			return fmt.Errorf("Snapshots.Next: %w", err)
		}

		for {
			doc, err := snap.Documents.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return fmt.Errorf("Documents.Next: %w", err)
			}

			var msg Message
			err = doc.DataTo(&msg)
			if err != nil {
				return fmt.Errorf("error converting document to message: %w", err)
			}

			// Lock the entire message processing flow
			mu.Lock()
			lastUserMessage = time.Now()

			// Generate response
			responseMessage, err := generateResponse(ctx, msg.Message, conversationSummary)
			if err != nil {
				mu.Unlock()
				return fmt.Errorf("error generating response: %w", err)
			}

			// Write response to Firestore
			err = writeMessage(ctx, client, pingCollection, doc.Ref.ID, responseMessage)
			if err != nil {
				mu.Unlock()
				return fmt.Errorf("error writing response message: %w", err)
			}

			// Mark the message as processed
			_, err = doc.Ref.Update(ctx, []firestore.Update{
				{Path: "processed", Value: true},
			})
			if err != nil {
				mu.Unlock()
				return fmt.Errorf("error marking message as processed: %w", err)
			}

			// Update last response time
			lastResponseTime = time.Now()
			fmt.Fprintf(w, "Response written: %v\n", responseMessage)

			// Unlock after everything is complete
			mu.Unlock()
		}
	}
}

func monitorAndRespond(ctx context.Context, w io.Writer, serviceAccountPath, userCollection, pingCollection, pollCollection string) error {
	sa := option.WithCredentialsFile(serviceAccountPath)
	app, err := firebase.NewApp(ctx, nil, sa)
	if err != nil {
		return fmt.Errorf("error initializing app: %w", err)
	}

	client, err := app.Firestore(ctx)
	if err != nil {
		return fmt.Errorf("error initializing Firestore: %w", err)
	}
	defer client.Close()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			mu.Lock()
			currentTime := time.Now()

			pollSummary, err := fetchPollStatus(ctx, client, pollCollection)
			if err != nil {
				mu.Unlock()
				return fmt.Errorf("error fetching poll status: %w", err)
			}

			updateConversationSummary(pollSummary)

			if currentTime.Sub(lastUserMessage) > 30*time.Second && currentTime.Sub(lastResponseTime) >= 10*time.Second {
				promptMessage, err := generateResponse(ctx, "prompt", conversationSummary)
				if err != nil {
					mu.Unlock()
					return fmt.Errorf("error generating prompt: %w", err)
				}

				err = writeMessage(ctx, client, pingCollection, "host-prompt", promptMessage)
				if err != nil {
					mu.Unlock()
					return fmt.Errorf("error writing prompt message: %w", err)
				}
				lastResponseTime = currentTime
			} else if currentTime.Sub(lastResponseTime) >= 15*time.Second {
				updateMessage := fmt.Sprintf("Poll update: %s", pollSummary)

				promptMessage, err := generateResponse(ctx, "poll-update", updateMessage)
				if err != nil {
					mu.Unlock()
					return fmt.Errorf("error generating prompt: %w", err)
				}

				err = writeMessage(ctx, client, pingCollection, "host-prompt", promptMessage)
				if err != nil {
					mu.Unlock()
					return fmt.Errorf("error writing prompt message: %w", err)
				}
				lastResponseTime = currentTime
			}

			mu.Unlock()
		}
	}
}

func fetchPollStatus(ctx context.Context, client *firestore.Client, pollCollection string) (string, error) {
	doc, err := client.Collection(pollCollection).Doc("q1").Get(ctx)
	if err != nil {
		return "", fmt.Errorf("error fetching poll document: %w", err)
	}

	var pollQuestion PollQuestion
	if err := doc.DataTo(&pollQuestion); err != nil {
		return "", fmt.Errorf("error converting document to PollQuestion: %w", err)
	}

	var summary string
	summary += fmt.Sprintf("Question: %s\n", pollQuestion.Question)
	for _, opt := range pollQuestion.Options {
		summary += fmt.Sprintf("%s - %s: %d votes\n", opt.Label, opt.OpText, len(opt.Voters))
	}
	return summary, nil
}

func updateConversationSummary(pollSummary string) {
	conversationSummary = fmt.Sprintf("Current poll status:\n%s\nConversation history: [Add relevant conversation history here]", pollSummary)
}

func generateResponse(ctx context.Context, userMessage, conversationSummary string) (string, error) {
	requestText := fmt.Sprintf("You're Amitabh Bachchan, hosting Kaun Banega Crorepati. Current status:\n%s\nUser said: %s\nRespond in Amitabh's style, max 30 words. Be witty and professional. Do not say anything that can be taken as abusive.", conversationSummary, userMessage)

	resp, err := model.Generate(ctx,
		ai.NewGenerateRequest(
			&ai.GenerationCommonConfig{Temperature: 1},
			ai.NewUserTextMessage(requestText)),
		nil)
	if err != nil {
		return "", fmt.Errorf("gemini model error: %w", err)
	}

	return resp.Text(), nil
}

func writeMessage(ctx context.Context, client *firestore.Client, collection, id, message string) error {
	_, err := client.Collection(collection).Doc(id).Set(ctx, Message{
		ID:        id,
		Message:   message,
		Timestamp: time.Now(),
		Processed: false,
	})
	return err
}
