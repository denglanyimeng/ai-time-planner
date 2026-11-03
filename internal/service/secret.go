package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"ai-time-planner/internal/ai"
	"ai-time-planner/internal/model"
)

// 加密后的密钥带此前缀，便于识别出历史遗留的明文值。
const secretPrefix = "enc:"

var (
	secretOnce sync.Once
	secretKey  []byte
)

// dataDir 返回本地数据目录，可用 PLANNER_DATA_DIR 覆盖。
func dataDir() string {
	if d := strings.TrimSpace(os.Getenv("PLANNER_DATA_DIR")); d != "" {
		return d
	}
	return "data"
}

// loadSecretKey 读取（或首次生成）用于加密 API Key 的本地密钥。
//
// 密钥只保存在本机文件中，不随备份导出，也不出现在任何接口响应里。
// 这样即使数据库被导出，API Key 依然是密文。
func loadSecretKey() []byte {
	secretOnce.Do(func() {
		dir := dataDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return
		}
		path := filepath.Join(dir, ".secret")

		if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
			secretKey = b
			return
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return
		}
		secretKey = key
	})
	return secretKey
}

// EncryptSecret 加密敏感字符串。空字符串返回空字符串。
// 密钥不可用时返回空串，宁可让用户重新填写，也不把明文写进数据库。
func EncryptSecret(plain string) string {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return ""
	}
	key := loadSecretKey()
	if len(key) != 32 {
		return ""
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ""
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ""
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return secretPrefix + base64.StdEncoding.EncodeToString(sealed)
}

// DecryptSecret 解密敏感字符串。
// 没有加密前缀的值按明文原样返回，兼容通过环境变量直接配置的密钥。
func DecryptSecret(stored string) string {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return ""
	}
	if !strings.HasPrefix(stored, secretPrefix) {
		return stored
	}

	key := loadSecretKey()
	if len(key) != 32 {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, secretPrefix))
	if err != nil {
		return ""
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ""
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return ""
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return ""
	}
	return string(plain)
}

// AIClient 返回当前生效的 AI 客户端。
//
// 优先级：数据库中的配置（用户在界面里填的）> 环境变量/ .env。
// 这样用户既能用 .env 快速起步，也能在界面里随时改，且以界面为准。
func (s *Service) AIClient() *ai.Client {
	var sc model.SystemConfig
	if err := s.db.First(&sc, 1).Error; err == nil {
		if key := DecryptSecret(sc.AIAPIKeyEncrypted); key != "" {
			base := strings.TrimSpace(sc.AIBaseURL)
			if base == "" {
				base = s.cfg.AI.BaseURL
			}
			modelName := strings.TrimSpace(sc.AIModel)
			if modelName == "" {
				modelName = s.cfg.AI.Model
			}
			return ai.New(base, key, modelName, s.cfg.AI.TimeoutSeconds)
		}
	}
	return s.ai
}
