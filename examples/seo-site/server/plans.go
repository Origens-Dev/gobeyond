package seosite

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/holbrookab/gobeyond/renderplan"
)

func LoadPlans(directory string) (map[string]*renderplan.Plan, error) {
	plans := make(map[string]*renderplan.Plan, 8)
	for _, routeID := range []string{
		HomeRouteID, AccountRouteID, ArticleRouteID, CategoryRouteID,
		EnglishArticleRouteID, FrenchArticleRouteID, LocationRouteID, ProductRouteID,
	} {
		path := filepath.Join(directory, routeID+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read render plan %s: %w", routeID, err)
		}
		plan, err := renderplan.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("parse render plan %s: %w", routeID, err)
		}
		plans[routeID] = plan
	}
	return plans, nil
}
