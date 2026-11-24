import xgboost as xgb
import json
import os

def get_model_info():
    if not os.path.exists("model.json"):
        return {"error": "Modelo no encontrado"}
        
    model = xgb.XGBClassifier()
    model.load_model("model.json")
    importances = model.feature_importances_
    features = ["SMA-20", "ATR-14", "RSI-14", "Dist PDH", "Dist PDL", "Volatilidad"]
    
    result = {}
    for i in range(len(features)):
        result[features[i]] = round(float(importances[i]) * 100, 2)
        
    return result

print(json.dumps(get_model_info()))