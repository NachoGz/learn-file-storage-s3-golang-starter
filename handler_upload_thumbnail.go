package main

import (
	"fmt"
	"net/http"
	"time"
	"io"
	"path/filepath"
	"os"
	"mime"
	"crypto/rand"
	"encoding/base64"
	

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}


	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	const maxMemory = 10 << 20
	r.ParseMultipartForm(maxMemory)

	// "thumbnail" should match the HTML form input name
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()
	
	
	media_type, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error parsing content type", nil)
		return
	}


	if media_type != "image/jpeg" && media_type != "image/png" {
		respondWithError(w, http.StatusBadRequest, "File must be an image", nil)
		return
	}


	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Error fetching video", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Authentication error", nil)
		return
	}

	file_extensions, err := mime.ExtensionsByType(media_type)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error extracting extension", err)
		return
	}

	// Create a 32-byte slice to hold the random data
	random_data := make([]byte, 32)

	// Read 32 random bytes and store them in random_data
	_, err = rand.Read(random_data)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to generate random bytes", err)
		return
	}

	random_string := base64.RawURLEncoding.EncodeToString(random_data)
	file_name := random_string + file_extensions[0]
	path := filepath.Join(cfg.assetsRoot, file_name)


	// create a new file in the path
	asset_file, err := os.Create(path)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating a new file", err)
		return
	}


	// copy the contents of file on the new asset_file
	_, err = io.Copy(asset_file, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying files", err)
		return
	}

    
	// update db so that the existing video record has a new thumbnail URL
	thumbnailString := fmt.Sprintf("http://localhost:%s/assets/%v", cfg.port, file_name)
	video.ThumbnailURL = &thumbnailString


	err	= cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Error updating video", err)
		return
	}
	
	updated_video := database.Video {
		ID:				videoID,
		CreatedAt:		video.CreatedAt,
		UpdatedAt:		time.Now(),
		ThumbnailURL:	&thumbnailString,
		VideoURL:		video.VideoURL,
	}
	respondWithJSON(w, http.StatusOK, updated_video)
}
