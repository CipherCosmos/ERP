package handlers

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gbos/internal/auth"
	"gbos/internal/db"
	"gbos/internal/models"
)

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type LoginResponse struct {
	Token string      `json:"token"`
	User  models.User `json:"user"`
}

func Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var user models.User
	var passwordHash string
	err := db.DB.QueryRow(`SELECT id, username, password_hash, role, store_id, is_active FROM users WHERE username = ?`, req.Username).
		Scan(&user.ID, &user.Username, &passwordHash, &user.Role, &user.StoreID, &user.IsActive)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	if !user.IsActive {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "account is disabled"})
		return
	}

	token, err := auth.SignToken(user.ID.String(), string(user.Role), user.StoreID.String(), 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue session"})
		return
	}
	db.DB.Exec(`INSERT OR REPLACE INTO sync_records (id, table_name, record_id, operation, store_id, status)
		VALUES (?, 'users', ?, 'login', ?, 'completed')`,
		uuid.New().String(), user.ID.String(), user.StoreID.String())

	user.CreatedAt = time.Now()
	user.UpdatedAt = time.Now()
	c.JSON(http.StatusOK, LoginResponse{Token: token, User: user})
}

func Register(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
		Role    string `json:"role" binding:"required"`
		StoreID string `json:"store_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	userID := uuid.New().String()
	_, err = db.DB.Exec(`INSERT INTO users (id, username, password_hash, role, store_id) VALUES (?, ?, ?, ?, ?)`,
		userID, req.Username, string(passwordHash), req.Role, req.StoreID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": userID, "username": req.Username, "role": req.Role})
}