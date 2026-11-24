import pandas as pd
import numpy as np
import yfinance as yf
import xgboost as xgb
from sklearn.model_selection import train_test_split
from sklearn.metrics import accuracy_score
import os

print("⏳ Descargando datos históricos de QQQ...")
df = yf.download("QQQ", period="20y", interval="1d", progress=False)
if isinstance(df.columns, pd.MultiIndex):
    df.columns = df.columns.get_level_values(0)
df.columns = [c.lower() for c in df.columns]

print("⚙️ Calculando indicadores...")
df['sma_20'] = df['close'].rolling(20).mean()
df['atr_14'] = (df['high'] - df['low']).rolling(14).mean()
delta = df['close'].diff()
gain = (delta.where(delta > 0, 0)).rolling(window=14).mean()
loss = (-delta.where(delta < 0, 0)).rolling(window=14).mean()
rs = gain / loss
df['rsi_14'] = 100 - (100 / (1 + rs))
df['dist_pdh'] = (df['close'] - df['high'].shift(1)) / df['close']
df['dist_pdl'] = (df['close'] - df['low'].shift(1)) / df['close']
df['volatility'] = df['close'].pct_change().rolling(14).std()
df['signal'] = np.where(df['close'] >= df['high'].shift(1) * 0.999, 1, 0)
df['entry'] = df['high'].shift(1)
df['sl'] = df['low'].shift(1)
df['tp'] = df['entry'] + (df['atr_14'] * 2)

def check_outcome(row, df, idx):
    if row['signal'] == 0: return np.nan
    for i in range(1, 4):
        if idx + i >= len(df): break
        future_day = df.iloc[idx + i]
        if future_day['low'] <= row['sl']: return 0
        if future_day['high'] >= row['tp']: return 1
    return 0

print("🎯 Etiquetando el pasado...")
df['target'] = [check_outcome(df.iloc[idx], df, idx) for idx in range(len(df))]

features_list = ['sma_20', 'atr_14', 'rsi_14', 'dist_pdh', 'dist_pdl', 'volatility']
ml_data = df[features_list + ['target']].dropna()
X = ml_data[features_list]
X.columns = ['f0', 'f1', 'f2', 'f3', 'f4', 'f5']
y = ml_data['target']

# Calcular los pesos (Los últimos días valen 1.0, los viejos valen 0.15)
# Suponiendo ~252 días de trading al año.
dias_totales = len(X)
pesos = np.linspace(0.15, 1.0, dias_totales) # Peso lineal desde 0.15 hasta 1.0

X_train, X_test, y_train, y_test, w_train, w_test = train_test_split(
    X, y, pesos, test_size=0.2, random_state=42
)

print(f"🧠 Entrenando Modelo A (Sin Pesos) con {len(X_train)} operaciones...")
modelo_A = xgb.XGBClassifier(n_estimators=100, max_depth=3, learning_rate=0.05, random_state=42)
modelo_A.fit(X_train, y_train)
acc_A = accuracy_score(y_test, modelo_A.predict(X_test))
print(f"✅ Accuracy Modelo A: {acc_A * 100:.2f}%")

print(f"🧠 Entrenando Modelo B (CON Pesos Recientes) con {len(X_train)} operaciones...")
modelo_B = xgb.XGBClassifier(n_estimators=100, max_depth=3, learning_rate=0.05, random_state=42)
modelo_B.fit(X_train, y_train, sample_weight=w_train)
acc_B = accuracy_score(y_test, modelo_B.predict(X_test))
print(f"✅ Accuracy Modelo B (Weighted): {acc_B * 100:.2f}%")

# Elegir el ganador y guardarlo
if acc_B > acc_A:
    print("🏆 ¡El Modelo B (Con Pesos) es mejor! Guardando este modelo...")
    modelo_B.save_model("model_temp.json")
else:
    print("🏆 El Modelo A (Sin Pesos) es mejor. Guardando este modelo...")
    modelo_A.save_model("model_temp.json")

if os.path.exists("model.json"):
    os.remove("model.json")
os.rename("model_temp.json", "model.json")
print("✅ ¡Modelo ganador guardado exitosamente!")