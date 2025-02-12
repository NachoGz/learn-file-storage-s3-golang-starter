package main

import (
	"net/http"
	"mime"
	"os"
	"os/exec"
	"io"
	"fmt"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
	"bytes"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
)


func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1 << 30)

	// Parse the multipart form
	err := r.ParseMultipartForm(1 << 30)
	if err != nil {
		http.Error(w, "File too large or invalid request", http.StatusRequestEntityTooLarge)
		return
	}
	
	
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

	fmt.Println("uploading video", videoID, "by user", userID)

	videoData, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Couldn't find video metadata", err)
		return
	}

	if videoData.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Video does not belong to this user", nil)
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse video file", err)
		return
	}
	defer file.Close()


	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type")) 
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error parsing content type", nil)
		return
	}

	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "The file must be a video file", nil)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating temp file", err)
		return
	}

	// clean-up
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()


	// copy the contents of file on the new tempFile
	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying files", err)
		return
	}

	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error setting the offset of tempFile", err)
		return
	}

	// creating fileKey
	// Create a 32-byte slice to hold the random data
	random_data := make([]byte, 32)

	// Read 32 random bytes and store them in random_data
	_, err = rand.Read(random_data)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to generate random bytes", err)
		return
	}

	// determine aspect ratio
	aspectRatio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to get aspect ratio", err)
		return
	}


	ratioToFolder := map[string]string{
		"16:9":		"landscape",
		"9:16":		"portrait",
		"other":	"other",
	}
	// Convert random bytes to hex and append it the extension and prepend it the ratio
	fileKey := fmt.Sprintf("%s/%s.mp4", ratioToFolder[aspectRatio], hex.EncodeToString(random_data))

	processedFilePath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error processing file", err)
		return
	}

	processedFile, err := os.Open(processedFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error reading processed file", err)
		return
	}
	defer processedFile.Close()

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:			&cfg.s3Bucket,
		Key:			&fileKey,
		Body:			processedFile,
		ContentType:	&mediaType,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error putting objects in a bucket", err)
		return
	}

	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, fileKey)
	videoData.VideoURL = &videoURL

	err	= cfg.db.UpdateVideo(videoData)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Error updating video", err)
		return
	}
	
	updated_video := database.Video {
		ID:				videoID,
		CreatedAt:		videoData.CreatedAt,
		UpdatedAt:		time.Now(),
		ThumbnailURL:	videoData.ThumbnailURL,
		VideoURL:		&videoURL,
	}

	respondWithJSON(w, http.StatusOK, updated_video)
}


func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	var outBuffer bytes.Buffer
	cmd.Stdout = &outBuffer	
	
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("error running ffprobe: %w", err)
	}

	type FFProbeOutput struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}

	// Parse the JSON output
	var probeOutput FFProbeOutput
	if err := json.Unmarshal(outBuffer.Bytes(), &probeOutput); err != nil {
		return "", fmt.Errorf("error parsing ffprobe output: %w", err)
	}

	// Ensure we have at least one stream
	if len(probeOutput.Streams) == 0 {
		return "", fmt.Errorf("no streams found in video file")
	}

	// Get width and height
	width := probeOutput.Streams[0].Width
	height := probeOutput.Streams[0].Height

	// Determine aspect ratio
	if width > height {
		return "16:9", nil
	} else if height > width {
		return "9:16", nil
	} else {
		return "other", nil
	}
}


func processVideoForFastStart(filePath string) (string, error) {
	outputFilePath := filePath + ".processing"

	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputFilePath)
	var outBuffer bytes.Buffer
	cmd.Stdout = &outBuffer	
	
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("error running ffprobe: %w", err)
	}

	return outputFilePath, nil
}