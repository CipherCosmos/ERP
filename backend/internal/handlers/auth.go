package handlers

import (
	"database/sql"
	"net/http"
	"time"

	"gbos/internal/auth"
	"gbos/internal/db"
	"gbos/internal/middleware"
	"gbos/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
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
		Role     string `json:"role" binding:"required"`
		StoreID  string `json:"store_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	allowedRoles := map[string]bool{
		"manager": true, "cashier": true, "warehouse": true, "helper": true,
	}
	if !allowedRoles[req.Role] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid role"})
		return
	}
	if !sessionMayAccessStore(c, req.StoreID) {
		abortStoreMismatch(c)
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

func ListUsers(c *gin.Context) {
	storeID := c.Query("store_id")
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id required"})
		return
	}
	if !sessionMayAccessStore(c, storeID) {
		abortStoreMismatch(c)
		return
	}

	rows, err := db.DB.Query(`SELECT id, username, role, store_id, is_active, created_at, updated_at
		FROM users WHERE store_id = ? ORDER BY username`, storeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	users := make([]map[string]interface{}, 0)
	for rows.Next() {
		var id, username, role, sid string
		var isActive bool
		var createdAt, updatedAt string
		rows.Scan(&id, &username, &role, &sid, &isActive, &createdAt, &updatedAt)
		users = append(users, map[string]interface{}{
			"id": id, "username": username, "role": role, "store_id": sid,
			"is_active": isActive, "created_at": createdAt, "updated_at": updatedAt,
		})
	}
	c.JSON(http.StatusOK, users)
}

func DeactivateUser(c *gin.Context) {
	id := c.Param("id")

	var targetStore string
	err := db.DB.QueryRow(`SELECT store_id FROM users WHERE id = ?`, id).Scan(&targetStore)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !sessionMayAccessStore(c, targetStore) {
		abortStoreMismatch(c)
		return
	}

	// Prevent deactivating self
	uid := middleware.GetUserID(c)
	if uid == id {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot deactivate yourself"})
		return
	}

	_, err = db.DB.Exec(`UPDATE users SET is_active = 0, updated_at = datetime('now') WHERE id = ?`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func ActivateUser(c *gin.Context) {
	id := c.Param("id")

	var targetStore string
	err := db.DB.QueryRow(`SELECT store_id FROM users WHERE id = ?`, id).Scan(&targetStore)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !sessionMayAccessStore(c, targetStore) {
		abortStoreMismatch(c)
		return
	}

	_, err = db.DB.Exec(`UPDATE users SET is_active = 1, updated_at = datetime('now') WHERE id = ?`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func ChangePassword(c *gin.Context) {
	var req struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	uid := middleware.GetUserID(c)
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	var currentHash string
	err := db.DB.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, uid).Scan(&currentHash)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(req.OldPassword)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "current password is incorrect"})
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	_, err = db.DB.Exec(`UPDATE users SET password_hash = ?, updated_at = datetime('now') WHERE id = ?`, string(newHash), uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

