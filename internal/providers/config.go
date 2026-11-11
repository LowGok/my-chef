package providers

import (
	"encoding/json"
	"errors"
	"os"
)

type Credentials struct {
	APIKey      string `json:"gemini_api_key"`
	RecipeModel string `json:"gemini_recipe_model"`
	ImageModel  string `json:"gemini_image_model"`
	CloudName   string `json:"cloudinary_cloud_name"`
	CloudKey    string `json:"cloudinary_api_key"`
	CloudSecret string `json:"cloudinary_api_secret"`
}

func LoadCredentials() (Credentials, error) {
	var c Credentials
	if path := os.Getenv("MYCHEF_CREDENTIALS"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return c, errors.New("credential file unavailable")
		}
		if json.Unmarshal(data, &c) != nil {
			return c, errors.New("credential file is not valid JSON")
		}
	}
	for key, target := range map[string]*string{"GEMINI_API_KEY": &c.APIKey, "MYCHEF_RECIPE_MODEL": &c.RecipeModel, "MYCHEF_IMAGE_MODEL": &c.ImageModel, "CLOUDINARY_CLOUD_NAME": &c.CloudName, "CLOUDINARY_API_KEY": &c.CloudKey, "CLOUDINARY_API_SECRET": &c.CloudSecret} {
		if v := os.Getenv(key); v != "" {
			*target = v
		}
	}
	if c.RecipeModel == "" {
		c.RecipeModel = "gemini-3.1-flash-lite"
	}
	if c.ImageModel == "" {
		c.ImageModel = "gemini-3.1-flash-image"
	}
	return c, nil
}
