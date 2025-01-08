package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v4"
	"github.com/joho/godotenv"
	"github.com/pgvector/pgvector-go"
)

type EmbeddingRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

type EmbeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func getEmbedding(input string) ([]float32, error) {
	openAIKey := os.Getenv("OPENAI_API_KEY")
	openAIEndpoint := os.Getenv("API_URL")
	reqBody, _ := json.Marshal(EmbeddingRequest{
		Input: input,
		Model: "text-embedding-ada-002",
	})

	req, _ := http.NewRequest("POST", openAIEndpoint, bytes.NewBuffer(reqBody))
	req.Header.Set("Authorization", "Bearer "+openAIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("リクエストエラー: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("APIエラー: %v - %s", resp.StatusCode, string(bodyBytes))
	}

	var embeddingResponse EmbeddingResponse
	err = json.NewDecoder(resp.Body).Decode(&embeddingResponse)
	if err != nil {
		return nil, fmt.Errorf("レスポンスのデコードエラー: %v", err)
	}

	// データが空かチェック
	if len(embeddingResponse.Data) == 0 {
		return nil, fmt.Errorf("Embeddingデータが返されていません")
	}

	return embeddingResponse.Data[0].Embedding, nil
}

func insertFAQ(conn *pgx.Conn, question, answer string) error {
	embedding, err := getEmbedding(question)
	if err != nil {
		log.Printf("Embedding生成エラー: %v", err)
		return fmt.Errorf("embedding generation failed: %v", err)
	}

	vectorEmbedding := pgvector.NewVector(embedding)

	_, err = conn.Exec(context.Background(), `
		INSERT INTO faqs (question, answer, embedding)
		VALUES ($1, $2, $3)
	`, question, answer, vectorEmbedding)

	if err != nil {
		log.Printf("FAQデータの登録エラー: %v", err)
		return err
	}

	log.Printf("FAQデータ登録完了: %s", question)
	return nil
}

// FAQを検索する関数
func searchFAQ(conn *pgx.Conn, query string) {
	embedding, err := getEmbedding(query)
	if err != nil {
		log.Printf("Embedding生成エラー: %v", err)
		return
	}

	vectorEmbedding := pgvector.NewVector(embedding)

	// 一番近いFAQを取得
	var question, answer string
	err = conn.QueryRow(context.Background(), `
		SELECT question, answer
		FROM faqs
		ORDER BY embedding <-> $1
		LIMIT 1
	`, vectorEmbedding).Scan(&question, &answer)

	if err != nil {
		fmt.Println("該当するFAQが見つかりませんでした。")
		return
	}

	fmt.Printf("Q: %s\nA: %s\n", question, answer)
}

func loadFAQsFromFile(filename string) ([]struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var faqs []struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
	}
	err = json.NewDecoder(file).Decode(&faqs)
	return faqs, err
}

func getExistingQuestions(conn *pgx.Conn) (map[string]bool, error) {
	rows, err := conn.Query(context.Background(), `SELECT question FROM faqs`)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch existing questions: %v", err)
	}
	defer rows.Close()

	existingQuestions := make(map[string]bool)
	for rows.Next() {
		var question string
		if err := rows.Scan(&question); err != nil {
			return nil, fmt.Errorf("Error scanning question: %v", err)
		}
		existingQuestions[question] = true
	}

	return existingQuestions, nil
}

func main() {

	//.env ファイルを読み込む
	err := godotenv.Load()
	if err != nil {
		log.Fatalf("Error loading .env file: %v", err)
	}

	url := fmt.Sprintf("postgres://%s:%s@%s:%s/%s", os.Getenv("POSTGRES_USER"),
		os.Getenv("POSTGRES_PW"), os.Getenv("POSTGRES_HOST"), os.Getenv("POSTGRES_PORT"), os.Getenv("POSTGRES_DB"))

	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer conn.Close(context.Background())

	// データベース内の既存の質問を取得
	existingQuestions, err := getExistingQuestions(conn)
	if err != nil {
		log.Fatalf("Error getting existing questions: %v", err)
	}
	// JSON から FAQ をロード
	faqs, err := loadFAQsFromFile("faqs.json")
	if err != nil {
		log.Fatalf("Error loading FAQs: %v", err)
	}

	// 新しい質問のみ登録
	for _, faq := range faqs {
		if _, exists := existingQuestions[faq.Question]; exists {
			log.Printf("既存の質問です。スキップ: %s", faq.Question)
			continue
		}

		err = insertFAQ(conn, faq.Question, faq.Answer)
		if err != nil {
			log.Printf("Error inserting FAQ: %v", err)
            return
		} 
	}

	fmt.Println("全てのFAQデータが登録されました。")

	// ユーザーから質問を受け取り検索を実行
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("質問を入力してください:")
	query, _ := reader.ReadString('\n')
	query = strings.TrimSpace(query)

	searchFAQ(conn, query)
}
