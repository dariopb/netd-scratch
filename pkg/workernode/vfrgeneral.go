package workernode

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	log "github.com/sirupsen/logrus"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type Command struct {
	Cmd   string
	Error bool
}

func ExecuteCommands(cmds []Command, onlyTry bool) error {
	for i := range cmds {
		log.Infof("  => %s -> %b", cmds[i].Cmd, cmds[i].Error)
		if !onlyTry {
			args := strings.Fields(cmds[i].Cmd)
			_, err := exec.Command(args[0], args[1:]...).Output()
			if err != nil {
				log.Errorf("Failed executing cmd: [%s]: %s", cmds[i], err.Error())
				if cmds[i].Error {
					return err
				}
			}
		}
	}

	if onlyTry {
		return fmt.Errorf("Debug mode")
	}
	return nil
}

func getNsName(id string, nicID string) string {
	return fmt.Sprintf("%s_alc_%s", "", nicID)
}

func getNicName(id string, nicID string) string {
	nicName := fmt.Sprintf("alc_%s", id)
	if nicID != "" {
		nicName = nicID
	}
	if len(nicName) > 14 {
		nicName = nicName[:14]
	}

	return nicName
}

func getRandomID() (string, error) {
	buff := make([]byte, 10)
	_, err := rand.Read(buff)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(buff), nil
}

func getHash(data string) string {
	h := fnv.New32a()
	h.Write([]byte(data))
	return fmt.Sprintf("%x", h.Sum32())
}

func getOrGenerateKeys(nicName string) (string, string, error) {
	log.Infof("Going to GetOrGenerateKeys for nic: %s", nicName)

	nsFinalFilename := fmt.Sprintf("./keys/%s", nicName)

	if _, err := os.Stat(nsFinalFilename + "_priv"); err == nil {
		log.Infof("    => found key file, using it")
	} else {
		key, err := wgtypes.GeneratePrivateKey()
		if err != nil {
			return "", "", err
		}

		os.Mkdir("./keys", 600)
		err = ioutil.WriteFile(nsFinalFilename+"_priv", []byte(key.String()), 0644)
		if err != nil {
			return "", "", err
		}

		pKey := key.PublicKey()
		err = ioutil.WriteFile(nsFinalFilename+"_pub", []byte(pKey.String()), 0644)
		if err != nil {
			return "", "", err
		}

		log.Errorf("Generated keypair: pubKey: [%s]", pKey.String())
	}

	buff, err := ioutil.ReadFile(nsFinalFilename + "_pub")
	if err != nil {
		return "", "", err
	}
	pubKey := strings.TrimSuffix(string(buff), "\n")
	log.Infof("Using keypair: pubKey: [%s]", pubKey)

	buff, err = ioutil.ReadFile(nsFinalFilename + "_priv")
	if err != nil {
		return "", "", err
	}
	privKey := strings.TrimSuffix(string(buff), "\n")

	return pubKey, privKey, nil
}
