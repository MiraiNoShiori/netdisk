package main

import (
	"context"
	"github.com/MiraiNoShiori/netdisk/internal/config"
	"github.com/MiraiNoShiori/netdisk/internal/repository"
	"log"
)

func main() {
	c, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	s, err := repository.New(context.Background(), c.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(context.Background()); err != nil {
		log.Fatal(err)
	}
	log.Print("数据库迁移完成")
}
