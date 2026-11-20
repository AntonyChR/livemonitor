package utils

import "os"

func SetTempUnixServerName(name string) error {
	if err := os.WriteFile("/tmp/ls.temp", []byte(name), 0644); err != nil {
		return err
	}
	println("nombre de socket guardado: ", name)
	return nil
}
