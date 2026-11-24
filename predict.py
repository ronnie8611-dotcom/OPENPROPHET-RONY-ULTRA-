import sys
import xgboost as xgb
import numpy as np

# Cargar el modelo
model = xgb.XGBClassifier()
model.load_model("model.json")

# Leer los 6 datos que envía Go
features = [float(x) for x in sys.argv[1:7]]

# Predecir (0 = Perder, 1 = Ganar)
pred = model.predict(np.array([features]))[0]
print(int(pred))