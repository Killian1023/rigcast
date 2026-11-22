package gpu

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"rigcast/internal/models"
)

func Status(ctx context.Context) ([]models.GPU, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=index,name,memory.used,memory.total,utilization.gpu,temperature.gpu", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi: %w", err)
	}
	rows := csv.NewReader(bytes.NewReader(out))
	rows.FieldsPerRecord = 6
	rows.TrimLeadingSpace = true
	all, err := rows.ReadAll()
	if err != nil {
		return nil, err
	}
	gpus := []models.GPU{}
	for _, row := range all {
		index, err := strconv.Atoi(strings.TrimSpace(row[0]))
		if err != nil {
			return nil, err
		}
		g := models.GPU{Index: index, Name: strings.TrimSpace(row[1])}
		for i, field := range []**int{&g.MemoryUsed, &g.MemoryTotal, &g.Utilization, &g.Temperature} {
			value := strings.TrimSpace(row[i+2])
			if value == "N/A" || strings.HasPrefix(value, "[") {
				continue
			}
			n, err := strconv.Atoi(value)
			if err != nil {
				return nil, err
			}
			*field = &n
		}
		gpus = append(gpus, g)
	}
	return gpus, nil
}
