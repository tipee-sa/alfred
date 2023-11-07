package main

import (
	"os/exec"
	"time"

	"github.com/docker/docker/client"
	"github.com/gammadia/alfred/proto"
	"github.com/gammadia/alfred/server/config"
	"github.com/gammadia/alfred/server/log"
	"github.com/samber/lo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) LoadImage(srv proto.Alfred_LoadImageServer) (err error) {
	log.Debug("Starting image load stream")
	defer func() { log.Debug("Finished image load stream", "error", err) }()

	// Wait for init message
	msg, err := srv.Recv()
	if err != nil {
		return err
	}

	switch msg := msg.Message.(type) {
	case *proto.LoadImageMessage_Init_:
		log.Debug("Got image load init", "image", msg.Init.ImageId)
		_, _, err = docker.ImageInspectWithRaw(srv.Context(), msg.Init.ImageId)
		if err != nil {
			if client.IsErrNotFound(err) {
				log.Debug("Image not found, sending continue")
				if err = srv.Send(&proto.LoadImageResponse{
					Status:    proto.LoadImageResponse_CONTINUE,
					ChunkSize: lo.ToPtr(uint32(config.MaxPacketSize - 1024*1024 /* leave 1MB of margin */)),
				}); err != nil {
					return
				}
				break
			} else {
				return err
			}
		} else {
			log.Debug("Image found, sending ok")
			if err = srv.Send(&proto.LoadImageResponse{
				Status: proto.LoadImageResponse_OK,
			}); err != nil {
				return
			}
			return nil
		}
	default:
		return status.Errorf(codes.InvalidArgument, "unexpected message type: %T", msg)
	}

	// Main load loop
	cmd := exec.Command("/bin/sh", "-c", "zstd --decompress | docker load")
	cmd.WaitDelay = 10 * time.Second

	writer := lo.Must(cmd.StdinPipe())
	if err := cmd.Start(); err != nil {
		return err
	}
	defer writer.Close()

	for {
		msg, err = srv.Recv()
		if err != nil {
			return err
		}

		switch msg := msg.Message.(type) {
		case *proto.LoadImageMessage_Data_:
			log.Debug("Got image load chunk", "length", msg.Data.Length)
			if _, err = writer.Write(msg.Data.Chunk[:msg.Data.Length]); err != nil {
				return err
			}
		case *proto.LoadImageMessage_Done_:
			log.Debug("Got image load done")
			writer.Close()
			return cmd.Wait()
		default:
			return status.Errorf(codes.InvalidArgument, "unexpected message type: %T", msg)
		}
	}
}
