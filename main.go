package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/websocket"
)

var ctx = context.Background()

var redisClient *redis.Client

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // allow all origins
}

type ClientManager struct {
	clients    map[*websocket.Conn]bool
	broadcast  chan string
	register   chan *websocket.Conn
	unregister chan *websocket.Conn
	mutex      sync.Mutex
}

func NewClientManager() *ClientManager {
	return &ClientManager{
		clients:    make(map[*websocket.Conn]bool), // empty map
		broadcast:  make(chan string),
		register:   make(chan *websocket.Conn),
		unregister: make(chan *websocket.Conn),
	}
}

// Start listens for events on the ClientManager
type message struct {
	User    string `json:"user"`
	Content string `json:"content"`
}

func (manager *ClientManager) Start() {
	for {
		select {
		case conn := <-manager.register:
			manager.mutex.Lock()
			manager.clients[conn] = true
			manager.mutex.Unlock()
			slog.Info("New client connected")
		case conn := <-manager.unregister:
			manager.mutex.Lock()
			if _, ok := manager.clients[conn]; ok {
				delete(manager.clients, conn)
				conn.Close()
				slog.Info("Client disconnected")
			}
			manager.mutex.Unlock()
		case message := <-manager.broadcast:
			manager.mutex.Lock()
			for conn := range manager.clients {
				err := conn.WriteJSON(message)
				if err != nil {
					slog.Info("Write error:" + err.Error())
					conn.Close()
					delete(manager.clients, conn)
				}
			}
			manager.mutex.Unlock()
		}
	}
}

func handleConnections(manager *ClientManager, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Info("WebSocket upgrade error:" + err.Error())
		return
	}
	defer conn.Close()

	manager.register <- conn

	for {
		var msg message
		if err := conn.ReadJSON(&msg); err != nil {
			slog.Info("Read error:" + err.Error())
			break
		}

		err := redisClient.RPush(ctx, "chat_messages", fmt.Sprintf("%s: %s", msg.User, msg.Content)).Err()
		if err != nil {
			slog.Error("Error saving to Redis:" + err.Error())
		} else {
			slog.Info("Message saved to Redis successfully")
		}
		manager.broadcast <- fmt.Sprintf("%s: %s", msg.User, msg.Content)
	}

	manager.unregister <- conn

}

func initRedis() {
	redisClient = redis.NewClient(&redis.Options{
		Addr: "redis:6379",
		DB:   0,
	})
	if _, err := redisClient.Ping(ctx).Result(); err != nil {
		slog.Error("Redis connection error:")
		panic(err)
	}
	slog.Info("Redis connected")

}

func main() {
	initRedis()

	manager := NewClientManager()
	go manager.Start()

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleConnections(manager, w, r)
	})

	port := "8080"
	slog.Info("Server started on port " + port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		slog.Error("ListenAndServe error:")
		panic(err)
	}
}
