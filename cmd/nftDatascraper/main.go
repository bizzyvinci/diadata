package main

import (
	"errors"
	"flag"
	"sync"
	"time"

	"github.com/jackc/pgconn"

	"github.com/diadata-org/diadata/pkg/dia"
	models "github.com/diadata-org/diadata/pkg/model"

	nftdatascrapers "github.com/diadata-org/diadata/pkg/dia/nft/nftData-scrapers"
	log "github.com/sirupsen/logrus"
)

func main() {

	wg := sync.WaitGroup{}

	rdb, err := models.NewRelDataStore()
	if err != nil {
		log.Fatal("relational datastore error: ", err)
	}

	scraperType := flag.String("nftclass", "Sorare", "which NFT class")
	flag.Parse()
	var scraper nftdatascrapers.NFTDataScraper

	switch *scraperType {
	case "Sorare":
		log.Println("NFT Data Scraper: Start scraping data from Sorare")
		scraper = nftdatascrapers.NewSorareScraper(rdb)
	case "CryptoPunks":
		log.Println("NFT Data Scraper: Start scraping data from CryptoPunks")
		scraper = nftdatascrapers.NewCryptoPunksScraper(rdb)
	case "Topshot":
		log.Println("NFT Data Scraper: Start scraping data from NBA Topshot")
		scraper = nftdatascrapers.NewNBATopshotScraper(rdb)
	case "CryptoKitties":
		log.Println("NFT Data Scraper: Start scraping data from CryptoKitties")
		scraper = nftdatascrapers.NewCryptoKittiesScraper(rdb)
	default:
		for {
			time.Sleep(24 * time.Hour)
		}
	}

	wg.Add(1)
	go handleData(scraper.GetDataChannel(), &wg, rdb)
	defer wg.Wait()

}

func handleData(dataChannel chan dia.NFT, wg *sync.WaitGroup, rdb *models.RelDB) {
	defer wg.Done()

	for {
		nft, ok := <-dataChannel
		if !ok {
			log.Error("error")
			return
		}
		log.Info("set nft: ", nft)
		err := rdb.SetNFT(nft)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				if pgErr.Code == "23505" {
					log.Infof("nft %s from class %s already in db. continue.", nft.TokenID, nft.NFTClass.Name)
					continue
				} else {
					log.Errorf("postgres error saving nft %s -- %s: %v", nft.NFTClass.Name, nft.TokenID, err)
				}
			} else {
				log.Errorf("Error saving nft from class %s with id %s: %v", nft.NFTClass.Name, nft.TokenID, err)
			}
		} else {
			log.Infof("successfully set nft %s with id: %s", nft.NFTClass.Name, nft.TokenID)
		}
	}

}
